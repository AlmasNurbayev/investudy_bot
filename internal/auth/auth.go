// Package auth — вход на сайт: пароли, сессии, ограничение попыток.
//
// Сессия — случайный токен в cookie; в базе лежит только его SHA-256, поэтому
// дамп базы не даёт готовых cookie. Срок скользящий: каждый запрос продлевает
// его на session_ttl_days из settings. Отзыв мгновенный — пользователь читается из базы на
// каждом запросе, а блокировка и смена пароля удаляют его сессии.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"investudy_bot/internal/model"
	"investudy_bot/internal/repository"
)

// touchEvery — как часто продлевать сессию. Не на каждом запросе: отчёт
// тянет несколько запросов подряд, и запись в базу на каждый из них
// ничего не добавляет к сроку сессии.
const touchEvery = 5 * time.Minute

var (
	// ErrBadCredentials — неверный логин или пароль. Одна ошибка на оба
	// случая: различать их значит подсказывать, какие логины существуют.
	ErrBadCredentials = errors.New("неверный логин или пароль")
	// ErrNoSession — сессии нет, она истекла или пользователь заблокирован.
	ErrNoSession = errors.New("сессия не найдена или истекла")
)

// TooManyAttempts — вход временно заперт после серии неудач.
type TooManyAttempts struct {
	RetryAfter time.Duration
}

func (e TooManyAttempts) Error() string {
	return fmt.Sprintf("слишком много неудачных попыток, повторите через %s", e.RetryAfter.Round(time.Second))
}

// Store — хранилище пользователей и сессий (реализует repository.Users).
type Store interface {
	AuthSettings(ctx context.Context) (model.AuthSettings, error)
	UserByLogin(ctx context.Context, login string) (model.User, string, error)
	CreateSession(ctx context.Context, tokenHash []byte, userID int64, now, expires time.Time, userAgent string) error
	SessionByToken(ctx context.Context, tokenHash []byte) (repository.Session, error)
	TouchSession(ctx context.Context, tokenHash []byte, now, expires time.Time) error
	DeleteSession(ctx context.Context, tokenHash []byte) error
}

type Service struct {
	store   Store
	limiter *limiter
	// dummy — хеш, с которым сверяется пароль несуществующего логина:
	// без этого неизвестный логин отвечал бы мгновенно, а известный — через
	// сотню миллисекунд argon2, и список логинов читался бы по времени ответа.
	dummy string
}

func New(store Store) (*Service, error) {
	dummy, err := HashPassword("dummy password for timing")
	if err != nil {
		return nil, err
	}

	return &Service{store: store, limiter: newLimiter(), dummy: dummy}, nil
}

// NormalizeLogin — логин без учёта регистра и пробелов по краям: «Almas»
// и «almas » — один человек.
func NormalizeLogin(login string) string {
	return strings.ToLower(strings.TrimSpace(login))
}

// Grant — выданная сессия.
type Grant struct {
	// Token — значение cookie.
	Token string
	// TTL — срок, на который выдана сессия: он же Max-Age cookie.
	TTL  time.Duration
	User model.User
}

// Login проверяет логин и пароль и открывает сессию. Кроме токена отдаёт
// пользователя — ответ входа несёт его данные.
func (s *Service) Login(
	ctx context.Context, login, password, ip, userAgent string, now time.Time,
) (Grant, error) {
	login = NormalizeLogin(login)
	key := login + "|" + ip

	// Настройки читаются сразу: лимит нужен до проверки пароля, срок — после.
	// Один запрос на попытку входа — по сравнению с argon2 это ничто.
	policy, err := LoadPolicy(ctx, s.store)
	if err != nil {
		return Grant{}, err
	}

	if locked, retry := s.limiter.blocked(key, now, policy.MaxFailures, policy.FailureWindow); locked {
		return Grant{}, TooManyAttempts{RetryAfter: retry}
	}

	user, hash, err := s.store.UserByLogin(ctx, login)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return Grant{}, err
	}

	if hash == "" {
		// Логина нет или пароль не задан — сверяем с пустышкой ради времени.
		hash = s.dummy
	}

	ok, err := VerifyPassword(hash, password)
	if err != nil {
		return Grant{}, fmt.Errorf("verify password of user %d: %w", user.ID, err)
	}

	if !ok || user.ID == 0 || user.BlockedAt.Valid || hash == s.dummy {
		s.limiter.fail(key, now, policy.FailureWindow)
		return Grant{}, ErrBadCredentials
	}

	s.limiter.reset(key)

	token, tokenHash, err := newToken()
	if err != nil {
		return Grant{}, err
	}

	if err = s.store.CreateSession(ctx, tokenHash, user.ID, now, now.Add(policy.SessionTTL), userAgent); err != nil {
		return Grant{}, err
	}

	return Grant{Token: token, TTL: policy.SessionTTL, User: user}, nil
}

// Authenticate находит пользователя по токену сессии. cookieTTL > 0 — срок
// продлён, и cookie стоит выдать заново с этим сроком (Max-Age).
func (s *Service) Authenticate(
	ctx context.Context, token string, now time.Time,
) (user model.User, cookieTTL time.Duration, err error) {
	if token == "" {
		return model.User{}, 0, ErrNoSession
	}

	hash := hashToken(token)

	sess, err := s.store.SessionByToken(ctx, hash)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return model.User{}, 0, ErrNoSession
		}

		return model.User{}, 0, err
	}

	// Блокировка удаляет сессии сама, но проверка повторяется здесь: правка
	// в базе руками сессии не трогает.
	if !now.Before(sess.ExpiresAt) || sess.User.BlockedAt.Valid {
		return model.User{}, 0, ErrNoSession
	}

	if now.Sub(sess.LastSeenAt) >= touchEvery {
		policy, err := LoadPolicy(ctx, s.store)
		if err != nil {
			return model.User{}, 0, err
		}

		if err = s.store.TouchSession(ctx, hash, now, now.Add(policy.SessionTTL)); err != nil {
			return model.User{}, 0, err
		}

		cookieTTL = policy.SessionTTL
	}

	return sess.User, cookieTTL, nil
}

// Logout гасит сессию токена. Неизвестный токен — не ошибка: выйти из
// уже погашенной сессии значит ничего не делать.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}

	return s.store.DeleteSession(ctx, hashToken(token))
}

// newToken — 32 случайных байта: в cookie base64, в базе SHA-256.
func newToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("token: %w", err)
	}

	token := base64.RawURLEncoding.EncodeToString(raw)

	return token, hashToken(token), nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

type userKey struct{}

// WithUser кладёт пользователя запроса в контекст — это делает middleware сессии.
func WithUser(ctx context.Context, u model.User) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

// UserFrom достаёт пользователя запроса. ok = false — запрос без сессии;
// до обработчиков, кроме входа, такой не доходит.
func UserFrom(ctx context.Context) (model.User, bool) {
	u, ok := ctx.Value(userKey{}).(model.User)
	return u, ok
}
