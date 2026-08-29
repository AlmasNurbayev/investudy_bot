package repository

import (
	"context"
	"fmt"
	"sort"

	"github.com/guregu/null/v6"
	"github.com/jackc/pgx/v5"

	"investudy_bot/internal/model"
)

// Beginner — источник транзакций (его реализует internal/db.Conn).
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Store — точка входа в запись данных парсера.
type Store struct {
	db Beginner
}

func NewStore(db Beginner) *Store {
	return &Store{db: db}
}

// Begin открывает транзакцию одного синка. Весь срез заливается внутри неё:
// до COMMIT ни строка snapshots, ни строки data другим сессиям не видны,
// поэтому недостроенная версия не может стать текущей.
func (s *Store) Begin(ctx context.Context) (*Tx, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}

	return &Tx{tx: tx, refs: make(map[string]map[string]int64)}, nil
}

// Tx — транзакция одного синка.
type Tx struct {
	tx pgx.Tx
	// refs кэширует id справочников в пределах транзакции: 70 тыс. строк ссылаются
	// на десятки значений, так что запросов уходит столько же, сколько уникальных имён.
	// Кэш живёт не дольше транзакции — при откате выданные id перестали бы существовать.
	refs map[string]map[string]int64
}

func (t *Tx) Commit(ctx context.Context) error   { return t.tx.Commit(ctx) }
func (t *Tx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

// BeginSnapshot заводит новую версию среза.
func (t *Tx) BeginSnapshot(ctx context.Context) (int64, error) {
	var id int64
	err := t.tx.QueryRow(ctx, `INSERT INTO snapshots DEFAULT VALUES RETURNING id`).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert snapshot: %w", err)
	}

	return id, nil
}

// FinishSnapshot фиксирует размер среза. Публикация происходит самим COMMIT.
func (t *Tx) FinishSnapshot(ctx context.Context, snapshotID, rowCount int64) error {
	_, err := t.tx.Exec(ctx,
		`UPDATE snapshots SET row_count = $2 WHERE id = $1`, snapshotID, rowCount)
	if err != nil {
		return fmt.Errorf("finish snapshot %d: %w", snapshotID, err)
	}

	return nil
}

var dataColumns = []string{
	"date", "num_oper", "type_oper",
	"debet_val", "credit_val", "ex_rate", "debet", "credit",
	"sender", "description", "bank", "period", "organization",
	"division_id", "item_id", "sub_item_id",
	"comment1", "comment2",
	"fin_type_id", "sum_dash", "vid_id",
	"sum_revenue", "sum_cost", "sum_return",
	"snapshot_id",
}

// InsertRows заливает срез целиком. Бизнес-ключ не нужен: строка не сматчивается
// со своей вчерашней версией, каждая версия пишется с нуля.
func (t *Tx) InsertRows(ctx context.Context, snapshotID int64, rows []model.Row) (int64, error) {
	type refIDs struct {
		division, item, subItem, finType, vid null.Int
	}

	// Справочники резолвятся заранее: внутри CopyFrom запросы к той же транзакции
	// делать нельзя, соединение занято потоком копирования.
	ids := make([]refIDs, len(rows))
	for i, row := range rows {
		var (
			r   refIDs
			err error
		)

		if r.division, err = t.refID(ctx, "divisions", row.Division); err != nil {
			return 0, err
		}
		if r.item, err = t.refID(ctx, "items", row.Item); err != nil {
			return 0, err
		}
		if r.subItem, err = t.subItemID(ctx, row.SubItem, r.item); err != nil {
			return 0, err
		}
		if r.finType, err = t.refID(ctx, "fin_types", row.FinType); err != nil {
			return 0, err
		}
		if r.vid, err = t.refID(ctx, "vids", row.Vid); err != nil {
			return 0, err
		}

		ids[i] = r
	}

	n, err := t.tx.CopyFrom(ctx,
		pgx.Identifier{"data"},
		dataColumns,
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			row, ref := rows[i], ids[i]

			return []any{
				row.Date, row.NumOper, row.TypeOper,
				row.DebetVal, row.CreditVal, row.ExRate, row.Debet, row.Credit,
				row.Sender, row.Description, row.Bank, row.Period, row.Organization,
				ref.division, ref.item, ref.subItem,
				row.Comment1, row.Comment2,
				ref.finType, row.SumDash, ref.vid,
				row.SumRevenue, row.SumCost, row.SumReturn,
				snapshotID,
			}, nil
		}),
	)
	if err != nil {
		return 0, fmt.Errorf("copy data: %w", err)
	}

	return n, nil
}

// refUpserts — апсерт справочника с возвратом id.
//
// Внимание: ON CONFLICT DO NOTHING RETURNING id на конфликте не возвращает ничего,
// поэтому здесь DO UPDATE с самоприсваиванием — он отдаёт id и при вставке, и при
// совпадении. Строк в справочниках десятки, лишняя запись роли не играет.
var refUpserts = map[string]string{
	"divisions": `INSERT INTO divisions (name) VALUES ($1)
	              ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
	"items": `INSERT INTO items (name) VALUES ($1)
	          ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
	"fin_types": `INSERT INTO fin_types (name) VALUES ($1)
	              ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
	"vids": `INSERT INTO vids (name) VALUES ($1)
	         ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
}

// refID возвращает id значения справочника, создавая его при необходимости.
// Пустое имя — это NULL: все FK в data nullable.
func (t *Tx) refID(ctx context.Context, table, name string) (null.Int, error) {
	if name == "" {
		return null.Int{}, nil
	}

	if id, ok := t.refs[table][name]; ok {
		return null.IntFrom(id), nil
	}

	query, ok := refUpserts[table]
	if !ok {
		return null.Int{}, fmt.Errorf("unknown reference table %q", table)
	}

	var id int64
	if err := t.tx.QueryRow(ctx, query, name).Scan(&id); err != nil {
		return null.Int{}, fmt.Errorf("upsert %s %q: %w", table, name, err)
	}

	t.cache(table, name, id)

	return null.IntFrom(id), nil
}

// subItemID апсертит подстатью. item_id проставляется только при вставке и при
// появлении родителя у уже известной подстатьи — затирать существующую связь
// пустым значением нельзя.
func (t *Tx) subItemID(ctx context.Context, name string, itemID null.Int) (null.Int, error) {
	if name == "" {
		return null.Int{}, nil
	}

	if id, ok := t.refs["sub_items"][name]; ok {
		return null.IntFrom(id), nil
	}

	const query = `
		INSERT INTO sub_items (name, item_id) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE
		   SET item_id = COALESCE(EXCLUDED.item_id, sub_items.item_id)
		RETURNING id`

	var id int64
	if err := t.tx.QueryRow(ctx, query, name, itemID).Scan(&id); err != nil {
		return null.Int{}, fmt.Errorf("upsert sub_items %q: %w", name, err)
	}

	t.cache("sub_items", name, id)

	return null.IntFrom(id), nil
}

func (t *Tx) cache(table, name string, id int64) {
	if t.refs[table] == nil {
		t.refs[table] = make(map[string]int64)
	}
	t.refs[table][name] = id
}

// SchemeMonthly — схема чистки истории: оставить все срезы за последние 30 дней
// и по одному, новейшему, за каждый предшествующий месяц.
const SchemeMonthly = "monthly"

// pruneMonthly реализует SchemeMonthly.
//
// Месяц берётся из generated-колонок year/month, а не из date_trunc: они уже
// посчитаны в местной зоне, покрыты индексом snapshots_picker_idx, и граница
// месяца получается ровно та же, что видит пользователь в селекторе версий.
//
// Текущий срез защищён по построению. Если он свежее 30 дней — его отсекает
// первое условие. Если загрузка давно стоит и он старше — он новейший в своём
// месяце, значит EXISTS ложно. Отдельная оговорка про «не трогать последний»
// не нужна.
const pruneMonthly = `
	DELETE FROM snapshots s
	WHERE s.taken_at < now() - interval '30 days'
	  AND EXISTS (
	      SELECT 1 FROM snapshots n
	      WHERE n.taken_at > s.taken_at
	        AND n.year  = s.year
	        AND n.month = s.month
	  )`

// pruneSchemes — доступные схемы чистки. Имя передаётся в prunedb флагом -scheme.
var pruneSchemes = map[string]string{
	SchemeMonthly: pruneMonthly,
}

// Schemes возвращает имена доступных схем — для подсказки в сообщении об ошибке.
func Schemes() []string {
	names := make([]string, 0, len(pruneSchemes))
	for name := range pruneSchemes {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

// Prune удаляет лишние версии по указанной схеме; строки data уносит ON DELETE CASCADE.
func (s *Store) Prune(ctx context.Context, scheme string) (int64, error) {
	query, ok := pruneSchemes[scheme]
	if !ok {
		return 0, fmt.Errorf("unknown scheme %q: доступны %v", scheme, Schemes())
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin prune tx: %w", err)
	}
	// После успешного Commit откат вернёт ErrTxClosed — это ожидаемо.
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("prune %s: %w", scheme, err)
	}

	if err = tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit prune: %w", err)
	}

	return tag.RowsAffected(), nil
}
