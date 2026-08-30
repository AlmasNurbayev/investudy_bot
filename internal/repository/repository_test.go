package repository_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/guregu/null/v6"
	"github.com/jackc/pgx/v5"

	"investudy_bot/internal/model"
	"investudy_bot/internal/repository"
)

// Тест требует пустой БД с накатанными миграциями:
//
//	TEST_DATABASE_URL=postgres://postgres:test@localhost:55433/investudy go test ./internal/repository/
//
// Без переменной пропускается, чтобы go test ./... оставался офлайновым.
func newStore(t *testing.T) (*repository.Store, *pgx.Conn) {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	// Каждый тест начинает с чистого листа.
	if _, err = conn.Exec(ctx, `TRUNCATE snapshots, divisions, items, sub_items, fin_types, vids CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	return repository.NewStore(beginner{conn}), conn
}

type beginner struct{ conn *pgx.Conn }

func (b beginner) Begin(ctx context.Context) (pgx.Tx, error) { return b.conn.Begin(ctx) }

func date(t *testing.T, s string) null.Time {
	t.Helper()

	v, err := time.Parse("02.01.2006", s)
	if err != nil {
		t.Fatalf("bad date %q: %v", s, err)
	}

	return null.TimeFrom(v)
}

func sampleRows(t *testing.T) []model.Row {
	t.Helper()

	return []model.Row{
		{
			Date:         date(t, "05.03.2026"),
			NumOper:      null.StringFrom("177"),
			Debet:        null.FloatFrom(46829),
			Bank:         null.StringFrom("Kaspi"),
			Period:       date(t, "01.03.2026"),
			Organization: null.StringFrom("Aligee"),
			Division:     "Отдел продаж",
			Item:         "Аренда",
			SubItem:      "Аренда офиса",
			FinType:      "расход",
			Vid:          "Офис",
			SumCost:      null.FloatFrom(46829),
		},
		{
			// Пустые справочники и суммы: все FK и NUMERIC должны стать NULL.
			Date:       date(t, "06.03.2026"),
			NumOper:    null.StringFrom("178"),
			Credit:     null.FloatFrom(1000),
			Bank:       null.StringFrom("Halyk"),
			Period:     date(t, "01.03.2026"),
			FinType:    "доход",
			SumRevenue: null.FloatFrom(1000),
		},
	}
}

func publish(t *testing.T, store *repository.Store, rows []model.Row) (int64, int64) {
	t.Helper()

	ctx := context.Background()

	tx, err := store.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id, err := tx.BeginSnapshot(ctx)
	if err != nil {
		t.Fatalf("begin snapshot: %v", err)
	}

	n, err := tx.InsertRows(ctx, id, rows)
	if err != nil {
		t.Fatalf("insert rows: %v", err)
	}

	if err = tx.FinishSnapshot(ctx, id, n); err != nil {
		t.Fatalf("finish snapshot: %v", err)
	}

	if err = tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	return id, n
}

func TestInsertRowsResolvesReferences(t *testing.T) {
	store, conn := newStore(t)
	ctx := context.Background()

	id, n := publish(t, store, sampleRows(t))
	if n != 2 {
		t.Fatalf("inserted %d rows, want 2", n)
	}

	var rowCount int
	if err := conn.QueryRow(ctx, `SELECT row_count FROM snapshots WHERE id = $1`, id).Scan(&rowCount); err != nil {
		t.Fatalf("read row_count: %v", err)
	}
	if rowCount != 2 {
		t.Errorf("row_count = %d, want 2", rowCount)
	}

	// Справочники разрезолвились в FK, а не легли строками.
	var division, item, subItem, finType, vid string
	err := conn.QueryRow(ctx, `
		SELECT dv.name, it.name, si.name, ft.name, vd.name
		FROM data d
		JOIN divisions dv ON dv.id = d.division_id
		JOIN items     it ON it.id = d.item_id
		JOIN sub_items si ON si.id = d.sub_item_id
		JOIN fin_types ft ON ft.id = d.fin_type_id
		JOIN vids      vd ON vd.id = d.vid_id
		WHERE d.num_oper = '177'`).Scan(&division, &item, &subItem, &finType, &vid)
	if err != nil {
		t.Fatalf("join references: %v", err)
	}

	if division != "Отдел продаж" || item != "Аренда" || subItem != "Аренда офиса" ||
		finType != "расход" || vid != "Офис" {
		t.Errorf("references resolved to %q/%q/%q/%q/%q", division, item, subItem, finType, vid)
	}

	// sub_items привязана к родительской статье.
	var parent string
	if err = conn.QueryRow(ctx, `
		SELECT it.name FROM sub_items si JOIN items it ON it.id = si.item_id
		WHERE si.name = 'Аренда офиса'`).Scan(&parent); err != nil {
		t.Fatalf("sub_item parent: %v", err)
	}
	if parent != "Аренда" {
		t.Errorf("sub_item parent = %q, want %q", parent, "Аренда")
	}

	// Пустые значения легли как NULL, а не как 0 и не как пустая строка в FK.
	var nullFKs, nullSums int
	if err = conn.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE division_id IS NULL AND item_id IS NULL AND vid_id IS NULL),
		       count(*) FILTER (WHERE debet IS NULL AND sum_cost IS NULL)
		FROM data WHERE num_oper = '178'`).Scan(&nullFKs, &nullSums); err != nil {
		t.Fatalf("check nulls: %v", err)
	}
	if nullFKs != 1 || nullSums != 1 {
		t.Errorf("empty cells did not become NULL: fks=%d sums=%d", nullFKs, nullSums)
	}

	// Числа доехали без искажения.
	var debet float64
	if err = conn.QueryRow(ctx, `SELECT debet FROM data WHERE num_oper = '177'`).Scan(&debet); err != nil {
		t.Fatalf("read debet: %v", err)
	}
	if debet != 46829 {
		t.Errorf("debet = %v, want 46829", debet)
	}
}

// Второй синк не должен ни задваивать справочники, ни задваивать data_current.
func TestSecondSyncReplacesCurrent(t *testing.T) {
	store, conn := newStore(t)
	ctx := context.Background()

	publish(t, store, sampleRows(t))
	second, _ := publish(t, store, sampleRows(t))

	var current, total int
	if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM data_current), (SELECT count(*) FROM data)`).
		Scan(&current, &total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if current != 2 {
		t.Errorf("data_current has %d rows, want 2", current)
	}
	if total != 4 {
		t.Errorf("data has %d rows, want 4 (обе версии)", total)
	}

	var divisions int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM divisions`).Scan(&divisions); err != nil {
		t.Fatalf("count divisions: %v", err)
	}
	if divisions != 1 {
		t.Errorf("divisions = %d, want 1 (апсерт, а не вставка)", divisions)
	}

	// Оба среза свежие, поэтому месячная схема не должна тронуть ни одного:
	// всё за последние 30 дней сохраняется целиком.
	deleted, err := store.Prune(ctx, repository.SchemeMonthly)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 0 {
		t.Errorf("pruned %d snapshots, want 0 (оба в 30-дневном окне)", deleted)
	}

	var newest int64
	if err = conn.QueryRow(ctx, `SELECT id FROM snapshots ORDER BY taken_at DESC LIMIT 1`).Scan(&newest); err != nil {
		t.Fatalf("read newest snapshot: %v", err)
	}
	if newest != second {
		t.Errorf("newest snapshot is %d, want %d", newest, second)
	}
}

// Месячная схема: всё за последние 30 дней плюс по одному новейшему срезу
// за каждый предшествующий месяц.
func TestPruneMonthly(t *testing.T) {
	store, conn := newStore(t)
	ctx := context.Background()

	// Даты строятся от начала месяца, чтобы пары гарантированно попадали
	// в один календарный месяц независимо от того, когда прогоняется тест.
	if _, err := conn.Exec(ctx, `
		INSERT INTO snapshots (taken_at) VALUES
		  (now()),                                                                -- окно 30 дней
		  (now() - interval '5 days'),                                            -- окно
		  (now() - interval '25 days'),                                           -- окно
		  (date_trunc('month', now()) - interval '3 months' + interval '5 days'), -- месяц A, старший
		  (date_trunc('month', now()) - interval '3 months' + interval '20 days'),-- месяц A, новейший
		  (date_trunc('month', now()) - interval '6 months' + interval '2 days')  -- месяц B, один
	`); err != nil {
		t.Fatalf("seed snapshots: %v", err)
	}

	deleted, err := store.Prune(ctx, repository.SchemeMonthly)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 1 {
		t.Errorf("pruned %d snapshots, want 1 (только старший из месяца A)", deleted)
	}

	var left int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM snapshots`).Scan(&left); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 5 {
		t.Errorf("осталось %d версий, want 5 (3 в окне + по одной на два месяца)", left)
	}

	// В месяце A должен выжить именно новейший срез.
	var survivorDay int
	if err = conn.QueryRow(ctx, `
		SELECT EXTRACT(DAY FROM (taken_at AT TIME ZONE 'Asia/Almaty'))::int
		FROM snapshots
		WHERE taken_at < now() - interval '30 days'
		  AND date_trunc('month', taken_at) = date_trunc('month', now() - interval '3 months')
	`).Scan(&survivorDay); err != nil {
		t.Fatalf("read survivor: %v", err)
	}
	if survivorDay != 21 {
		t.Errorf("в месяце A выжил срез %d-го числа, want 21 (начало месяца + 20 дней)", survivorDay)
	}
}

// Денежные колонки объявлены как NUMERIC(17,2). Это даёт две гарантии, которые
// легко потерять при правке схемы, поэтому они закреплены тестом.
func TestNumericPrecision(t *testing.T) {
	store, conn := newStore(t)
	ctx := context.Background()

	publish(t, store, sampleRows(t))

	// 1. Масштаб сохраняется: без scale значение легло бы как «46829»,
	//    и отчёты показывали бы разное число знаков в соседних строках.
	var debet, sumCost string
	if err := conn.QueryRow(ctx,
		`SELECT debet::text, sum_cost::text FROM data WHERE num_oper = '177'`).
		Scan(&debet, &sumCost); err != nil {
		t.Fatalf("read amounts: %v", err)
	}
	if debet != "46829.00" || sumCost != "46829.00" {
		t.Errorf("debet=%q sum_cost=%q, want обе %q", debet, sumCost, "46829.00")
	}

	// 2. Значение за пределами 17 значащих цифр отвергается ошибкой, а не
	//    искажается молча. Именно там float64 перестаёт быть точным, поэтому
	//    граница колонки закрывает его обрыв по построению.
	rows := sampleRows(t)
	rows[0].Debet = null.FloatFrom(1e16) // 17 цифр до запятой — не влезает в (17,2)

	tx, err := store.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id, err := tx.BeginSnapshot(ctx)
	if err != nil {
		t.Fatalf("begin snapshot: %v", err)
	}
	if _, err = tx.InsertRows(ctx, id, rows); err == nil {
		t.Fatal("переполнение NUMERIC(17,2) прошло молча, ожидалась ошибка")
	}
}

// Неизвестная схема должна отвергаться, а не молча ничего не делать.
func TestPruneRejectsUnknownScheme(t *testing.T) {
	store, _ := newStore(t)

	if _, err := store.Prune(context.Background(), "weekly"); err == nil {
		t.Fatal("expected error for unknown scheme")
	}
}

// Откат транзакции не должен оставлять ни версии, ни строк, ни справочников.
func TestRollbackLeavesNothing(t *testing.T) {
	store, conn := newStore(t)
	ctx := context.Background()

	tx, err := store.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	id, err := tx.BeginSnapshot(ctx)
	if err != nil {
		t.Fatalf("begin snapshot: %v", err)
	}
	if _, err = tx.InsertRows(ctx, id, sampleRows(t)); err != nil {
		t.Fatalf("insert rows: %v", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	var snapshots, rows, divisions int
	if err = conn.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM snapshots), (SELECT count(*) FROM data), (SELECT count(*) FROM divisions)`).
		Scan(&snapshots, &rows, &divisions); err != nil {
		t.Fatalf("count: %v", err)
	}

	if snapshots != 0 || rows != 0 || divisions != 0 {
		t.Errorf("rollback left snapshots=%d data=%d divisions=%d, want all zero",
			snapshots, rows, divisions)
	}
}
