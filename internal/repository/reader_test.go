package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/guregu/null/v6"
	"github.com/jackc/pgx/v5"

	"investudy_bot/internal/lib/money"
	"investudy_bot/internal/model"
	"investudy_bot/internal/repository"
)

// newReader поднимает читающий репозиторий на той же базе, что и newStore.
//
// Таблицы бота чистятся отдельно: newStore про них не знает, а строка настроек
// приезжает миграцией, и восстанавливать её после TRUNCATE пришлось бы руками.
func newReader(t *testing.T) (*repository.Reader, *repository.Store, *pgx.Conn) {
	t.Helper()

	store, conn := newStore(t)

	if _, err := conn.Exec(context.Background(), `TRUNCATE users`); err != nil {
		t.Fatalf("truncate users: %v", err)
	}

	return repository.NewReader(querier{conn}), store, conn
}

type querier struct{ conn *pgx.Conn }

func (q querier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return q.conn.Query(ctx, sql, args...)
}

func (q querier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return q.conn.QueryRow(ctx, sql, args...)
}

func march(t *testing.T) (from, to time.Time) {
	t.Helper()

	return time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
}

// reportRows публикует срез и считает по нему сводку за март.
func reportRows(t *testing.T, rows []model.Row, excluded []string) []model.ReportRow {
	t.Helper()

	reader, store, _ := newReader(t)
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
		t.Fatalf("insert: %v", err)
	}
	if err = tx.FinishSnapshot(ctx, id, n); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	from, to := march(t)

	report, err := reader.ClosedReport(ctx, id, from, to, excluded)
	if err != nil {
		t.Fatalf("closed report: %v", err)
	}

	return report
}

func reportRow(t *testing.T, sum null.Float, division, item, subItem string) model.Row {
	t.Helper()

	return model.Row{
		Date:     date(t, "05.03.2026"),
		Period:   date(t, "01.03.2026"),
		Debet:    sum,
		Division: division,
		Item:     item,
		SubItem:  subItem,
	}
}

func TestClosedReportGroupsAndSums(t *testing.T) {
	rows := []model.Row{
		reportRow(t, null.FloatFrom(100), "Алматы", "Аренда", "Офис"),
		reportRow(t, null.FloatFrom(50.5), "Алматы", "Аренда", "Офис"),
		reportRow(t, null.FloatFrom(70), "Алматы", "Аренда", "Склад"),
		reportRow(t, null.FloatFrom(10), "Астана", "Аренда", "Офис"),
	}

	report := reportRows(t, rows, nil)

	if len(report) != 3 {
		t.Fatalf("строк %d, want 3: %+v", len(report), report)
	}

	// ORDER BY в запросе — контракт: renderDivision группирует одним проходом
	// и на неотсортированной выборке рассыпет иерархию.
	want := []struct{ division, item, subItem, debet string }{
		{"Алматы", "Аренда", "Офис", "150,50"},
		{"Алматы", "Аренда", "Склад", "70,00"},
		{"Астана", "Аренда", "Офис", "10,00"},
	}
	for i, w := range want {
		got := report[i]
		if got.Division != w.division || got.Item != w.item || got.SubItem != w.subItem {
			t.Errorf("строка %d = %s/%s/%s, want %s/%s/%s",
				i, got.Division, got.Item, got.SubItem, w.division, w.item, w.subItem)
		}
		if d := money.Format(got.Debet); d != w.debet {
			t.Errorf("строка %d: дебет %s, want %s", i, d, w.debet)
		}
	}
}

// Ради этого исключения отчёт и заводился: внутреннее движение денег не доход
// и не расход, и без отсечения итоги задваиваются.
func TestClosedReportExcludesItems(t *testing.T) {
	rows := []model.Row{
		reportRow(t, null.FloatFrom(100), "Алматы", "Аренда", "Офис"),
		reportRow(t, null.FloatFrom(999), "Алматы", "Пополнение", "Касса"),
	}

	// В настройке статья записана строчными, в листе — с заглавной: сравнение
	// обязано идти без учёта регистра, иначе исключение молча не сработает.
	report := reportRows(t, rows, []string{"пополнение"})

	if len(report) != 1 {
		t.Fatalf("строк %d, want 1: %+v", len(report), report)
	}
	if report[0].Item != "Аренда" {
		t.Errorf("осталась статья %q, want Аренда", report[0].Item)
	}
}

// Строки без аналитики парсер специально считает и докладывает о них
// администратору; прятать их в отчёте было бы непоследовательно.
func TestClosedReportKeepsRowsWithoutAnalytics(t *testing.T) {
	rows := []model.Row{reportRow(t, null.FloatFrom(100), "", "", "")}

	report := reportRows(t, rows, []string{"пополнение"})

	if len(report) != 1 {
		t.Fatalf("строк %d, want 1: %+v", len(report), report)
	}
	if report[0].Division != "—" || report[0].Item != "—" || report[0].SubItem != "—" {
		t.Errorf("пустая аналитика показана как %+v, ожидались прочерки", report[0])
	}
}

// Границы полуинтервала: нижняя входит, верхняя нет — иначе соседние месяцы
// пересекутся и одна проводка попадёт в оба отчёта.
func TestClosedReportPeriodBoundsAreHalfOpen(t *testing.T) {
	rows := []model.Row{
		{Date: date(t, "05.02.2026"), Period: date(t, "01.02.2026"), Debet: null.FloatFrom(1), Division: "Алматы", Item: "Аренда"},
		{Date: date(t, "05.03.2026"), Period: date(t, "01.03.2026"), Debet: null.FloatFrom(2), Division: "Алматы", Item: "Аренда"},
		{Date: date(t, "05.04.2026"), Period: date(t, "01.04.2026"), Debet: null.FloatFrom(4), Division: "Алматы", Item: "Аренда"},
	}

	report := reportRows(t, rows, nil)

	if len(report) != 1 {
		t.Fatalf("строк %d, want 1: %+v", len(report), report)
	}
	if got := money.Format(report[0].Debet); got != "2,00" {
		t.Errorf("дебет %s, want 2,00 — в период попали соседние месяцы", got)
	}
}

// Второй синк не должен задваивать суммы: отчёт считается по одной версии.
func TestClosedReportIsScopedToOneSnapshot(t *testing.T) {
	reader, store, _ := newReader(t)
	ctx := context.Background()

	publishOne := func(sum float64) int64 {
		tx, err := store.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		id, err := tx.BeginSnapshot(ctx)
		if err != nil {
			t.Fatalf("begin snapshot: %v", err)
		}
		rows := []model.Row{reportRow(t, null.FloatFrom(sum), "Алматы", "Аренда", "Офис")}
		n, err := tx.InsertRows(ctx, id, rows)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		if err = tx.FinishSnapshot(ctx, id, n); err != nil {
			t.Fatalf("finish: %v", err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}

		return id
	}

	publishOne(100)
	second := publishOne(7)

	from, to := march(t)

	report, err := reader.ClosedReport(ctx, second, from, to, nil)
	if err != nil {
		t.Fatalf("closed report: %v", err)
	}

	if len(report) != 1 {
		t.Fatalf("строк %d, want 1: %+v", len(report), report)
	}
	if got := money.Format(report[0].Debet); got != "7,00" {
		t.Errorf("дебет %s, want 7,00 — версии просуммировались", got)
	}

	// А выбор рабочей версии обязан отдать именно её.
	snapshots, err := reader.ListSnapshots(ctx, 10)
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	if len(snapshots) != 2 || snapshots[0].ID != second {
		t.Errorf("ListSnapshots = %+v, ожидались две версии, новейшая первой", snapshots)
	}
	if !snapshots[0].RowCount.Valid || snapshots[0].RowCount.Int64 != 1 {
		t.Errorf("row_count = %v, want 1", snapshots[0].RowCount)
	}
}

// Настройка приезжает миграцией: без неё отчёт посчитался бы с внутренними
// переводами внутри, и это не должно быть тихим отказом.
func TestClosedReportsSettingsComeFromMigration(t *testing.T) {
	reader, _, _ := newReader(t)

	cfg, err := reader.ClosedReportsSettings(context.Background())
	if err != nil {
		t.Fatalf("settings: %v", err)
	}

	want := map[string]bool{
		"пополнение": true, "перевод": true,
		"пополнение на оператора": true, "движение денег": true,
	}
	if len(cfg.ExcludedItems) != len(want) {
		t.Fatalf("исключений %d, want %d: %v", len(cfg.ExcludedItems), len(want), cfg.ExcludedItems)
	}
	for _, item := range cfg.ExcludedItems {
		if !want[item] {
			t.Errorf("неожиданное исключение %q", item)
		}
	}
}

func TestUserAllowed(t *testing.T) {
	reader, _, conn := newReader(t)
	ctx := context.Background()

	allowed, err := reader.UserAllowed(ctx, 42)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if allowed {
		t.Error("посторонний пущен к финансовым данным")
	}

	if _, err = conn.Exec(ctx, `INSERT INTO users (telegram_id, username) VALUES (42, 'almas')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	if allowed, err = reader.UserAllowed(ctx, 42); err != nil || !allowed {
		t.Errorf("выданный доступ не сработал: allowed=%v err=%v", allowed, err)
	}
}
