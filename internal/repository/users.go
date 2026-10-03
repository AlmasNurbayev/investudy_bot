package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"investudy_bot/internal/model"
)

var (
	// ErrNotFound — строки нет: пользователь, сессия.
	ErrNotFound = errors.New("not found")
	// ErrConflict — нарушена уникальность: логин или Telegram-id уже заняты.
	ErrConflict = errors.New("conflict")
)

// UserAllowed проверяет, есть ли пользователь в белом списке и не заблокирован ли он.
//
// Таблица users общая с сайтом, поэтому blocked_at проверяется и здесь:
// заблокированный на сайте не должен читать отчёт из Telegram.
//
// Администратор из TELEGRAM_ADMIN_ID сюда не попадает и проверяется отдельно
// в боте: сразу после миграции таблица пуста, и без такого обхода выдать доступ
// первому пользователю было бы некому.
func (r *Reader) UserAllowed(ctx context.Context, telegramID int64) (bool, error) {
	const query = `SELECT EXISTS (SELECT 1 FROM users WHERE telegram_id = $1 AND blocked_at IS NULL)`

	var allowed bool
	if err := r.db.QueryRow(ctx, query, telegramID).Scan(&allowed); err != nil {
		return false, fmt.Errorf("check user %d: %w", telegramID, err)
	}

	return allowed, nil
}

// TxQuerier — источник запросов и транзакций (его реализует internal/db.Pool).
type TxQuerier interface {
	Querier
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Users — пользователи и сессии сайта.
//
// Отдельно от Reader: Reader только читает, а здесь сайт пишет — users и
// sessions, то, чего в листе нет. В data отсюда не пишется ничего.
type Users struct {
	db TxQuerier
}

func NewUsers(db TxQuerier) *Users {
	return &Users{db: db}
}

// userColumns — общий SELECT пользователя. last_seen_at — по всем сессиям:
// администратору важно, когда человек заходил, а не через какое устройство.
const userColumns = `
	u.id, u.login, u.telegram_id, u.username, u.role,
	u.division_id, dv.name, u.is_admin, u.password_hash IS NOT NULL,
	u.blocked_at, (SELECT max(s.last_seen_at) FROM sessions s WHERE s.user_id = u.id),
	u.created_at, u.updated_at`

const userFrom = `FROM users u LEFT JOIN divisions dv ON dv.id = u.division_id`

func scanUser(row pgx.Row, extra ...any) (model.User, error) {
	var u model.User

	dest := []any{
		&u.ID, &u.Login, &u.TelegramID, &u.Username, &u.Role,
		&u.DivisionID, &u.DivisionName, &u.IsAdmin, &u.HasPassword,
		&u.BlockedAt, &u.LastSeenAt, &u.CreatedAt, &u.UpdatedAt,
	}

	if err := row.Scan(append(dest, extra...)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.User{}, ErrNotFound
		}

		return model.User{}, err
	}

	return u, nil
}

// ListUsers — все пользователи, заблокированные тоже: кто и когда имел
// доступ, должно оставаться видно.
func (r *Users) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := r.db.Query(ctx, `SELECT `+userColumns+` `+userFrom+` ORDER BY u.id`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []model.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}

		users = append(users, u)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}

	return users, nil
}

// UserByID — пользователь по id; ErrNotFound, если нет.
func (r *Users) UserByID(ctx context.Context, id int64) (model.User, error) {
	u, err := scanUser(r.db.QueryRow(ctx, `SELECT `+userColumns+` `+userFrom+` WHERE u.id = $1`, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return model.User{}, fmt.Errorf("user %d: %w", id, err)
	}

	return u, err
}

// UserByLogin — пользователь и хеш его пароля для входа; ErrNotFound, если
// логина нет. Хеш пустой, если пароль не задан.
func (r *Users) UserByLogin(ctx context.Context, login string) (model.User, string, error) {
	var hash *string

	u, err := scanUser(
		r.db.QueryRow(ctx, `SELECT `+userColumns+`, u.password_hash `+userFrom+` WHERE u.login = $1`, login),
		&hash,
	)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return model.User{}, "", err
		}

		return model.User{}, "", fmt.Errorf("user by login: %w", err)
	}

	if hash == nil {
		return u, "", nil
	}

	return u, *hash, nil
}

// CreateUser заводит пользователя; hash — хеш пароля или "" (пароля нет).
func (r *Users) CreateUser(ctx context.Context, in model.UserInput, hash string) (int64, error) {
	const query = `
		INSERT INTO users (login, telegram_id, username, role, division_id, is_admin, password_hash, blocked_at)
		VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''), CASE WHEN $8 THEN now() END)
		RETURNING id`

	var id int64
	err := r.db.QueryRow(ctx, query,
		in.Login, in.TelegramID, in.Username, in.Role, in.DivisionID, in.IsAdmin, hash, in.Blocked,
	).Scan(&id)
	if err != nil {
		return 0, conflict(err, "create user")
	}

	return id, nil
}

// UpdateCheck решает, допустима ли правка, глядя на пользователя до неё и
// на действующих администраторов. Вызывается внутри транзакции, под
// блокировкой их строк: две одновременные правки иначе прошли бы проверку
// «последний администратор» каждая по отдельности.
type UpdateCheck func(before model.User, activeAdmins []int64) error

// UpdateUser переписывает изменяемые поля пользователя.
//
// Блокировка гасит все сессии пользователя в той же транзакции: следующий
// его запрос получит 401, не дожидаясь проверки blocked_at. Снятая блокировка
// время прежней не сохраняет — это новая выдача доступа.
func (r *Users) UpdateUser(ctx context.Context, id int64, in model.UserInput, check UpdateCheck) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		admins, err := lockActiveAdmins(ctx, tx)
		if err != nil {
			return err
		}

		before, err := scanUser(tx.QueryRow(ctx,
			`SELECT `+userColumns+` `+userFrom+` WHERE u.id = $1 FOR UPDATE OF u`, id))
		if err != nil {
			return err
		}

		if err = check(before, admins); err != nil {
			return err
		}

		const update = `
			UPDATE users SET
				login = $2, telegram_id = $3, username = $4, role = $5, division_id = $6,
				is_admin = $7,
				blocked_at = CASE WHEN $8 THEN coalesce(blocked_at, now()) END,
				updated_at = now()
			WHERE id = $1`

		if _, err = tx.Exec(ctx, update,
			id, in.Login, in.TelegramID, in.Username, in.Role, in.DivisionID, in.IsAdmin, in.Blocked,
		); err != nil {
			return conflict(err, "update user")
		}

		if in.Blocked {
			if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
				return fmt.Errorf("revoke sessions: %w", err)
			}
		}

		return nil
	})
}

// SetPassword задаёт хеш пароля и гасит все сессии пользователя: смена
// пароля — обычно реакция на утечку, и старые входы должны перестать работать.
func (r *Users) SetPassword(ctx context.Context, id int64, hash string) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, id, hash)
		if err != nil {
			return fmt.Errorf("set password: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}

		if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}

		return nil
	})
}

// Divisions — справочник подразделений для выбора у руководителя отдела.
func (r *Users) Divisions(ctx context.Context) ([]model.Ref, error) {
	rows, err := r.db.Query(ctx, `SELECT id, name FROM divisions ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list divisions: %w", err)
	}
	defer rows.Close()

	var out []model.Ref
	for rows.Next() {
		var d model.Ref
		if err = rows.Scan(&d.ID, &d.Name); err != nil {
			return nil, fmt.Errorf("scan division: %w", err)
		}

		out = append(out, d)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list divisions: %w", err)
	}

	return out, nil
}

// CreateSession сохраняет сессию; в базе только хеш токена.
func (r *Users) CreateSession(
	ctx context.Context, tokenHash []byte, userID int64, now, expires time.Time, userAgent string,
) error {
	const query = `
		INSERT INTO sessions (token_hash, user_id, created_at, last_seen_at, expires_at, user_agent)
		VALUES ($1, $2, $3, $3, $4, nullif($5, ''))`

	if _, err := r.db.Exec(ctx, query, tokenHash, userID, now, expires, userAgent); err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	// Просроченные сессии того же пользователя убираются здесь же, при
	// входе: отдельная чистка по расписанию ради таблицы на десяток строк
	// не нужна.
	if _, err := r.db.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND expires_at < $2`, userID, now); err != nil {
		return fmt.Errorf("purge sessions: %w", err)
	}

	return nil
}

// Session — сессия с владельцем.
type Session struct {
	User       model.User
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// SessionByToken находит сессию по хешу токена; ErrNotFound, если нет.
// Пользователь читается заново на каждом запросе: роль, подразделение и
// блокировка действуют сразу, без перевыпуска сессии.
func (r *Users) SessionByToken(ctx context.Context, tokenHash []byte) (Session, error) {
	var s Session

	u, err := scanUser(r.db.QueryRow(ctx,
		`SELECT `+userColumns+`, s.last_seen_at, s.expires_at
		 FROM sessions s JOIN users u ON u.id = s.user_id LEFT JOIN divisions dv ON dv.id = u.division_id
		 WHERE s.token_hash = $1`, tokenHash),
		&s.LastSeenAt, &s.ExpiresAt)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Session{}, err
		}

		return Session{}, fmt.Errorf("session: %w", err)
	}

	s.User = u

	return s, nil
}

// TouchSession продлевает скользящий срок сессии.
func (r *Users) TouchSession(ctx context.Context, tokenHash []byte, now, expires time.Time) error {
	if _, err := r.db.Exec(ctx,
		`UPDATE sessions SET last_seen_at = $2, expires_at = $3 WHERE token_hash = $1`,
		tokenHash, now, expires); err != nil {
		return fmt.Errorf("touch session: %w", err)
	}

	return nil
}

// DeleteSession гасит одну сессию — выход.
func (r *Users) DeleteSession(ctx context.Context, tokenHash []byte) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	return nil
}

// lockActiveAdmins блокирует строки действующих администраторов до конца
// транзакции и отдаёт их id.
func lockActiveAdmins(ctx context.Context, tx pgx.Tx) ([]int64, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM users WHERE is_admin AND blocked_at IS NULL ORDER BY id FOR UPDATE`)
	if err != nil {
		return nil, fmt.Errorf("lock admins: %w", err)
	}

	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("lock admins: %w", err)
	}

	return ids, nil
}

func (r *Users) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	// После Commit откат ничего не делает, поэтому defer безопасен на обоих путях.
	defer func() { _ = tx.Rollback(ctx) }()

	if err = fn(tx); err != nil {
		return err
	}

	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// conflict переводит нарушение UNIQUE в ErrConflict: занятый логин —
// ошибка администратора, а не сбой сервиса, и отвечать на неё надо 409.
func conflict(err error, op string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
		return fmt.Errorf("%s: %w (%s)", op, ErrConflict, pgErr.ConstraintName)
	}

	return fmt.Errorf("%s: %w", op, err)
}
