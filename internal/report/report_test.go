package report

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/guregu/null/v6"
	"github.com/jackc/pgx/v5/pgtype"

	"investudy_bot/internal/access"
	"investudy_bot/internal/lib/money"
	"investudy_bot/internal/lib/period"
	"investudy_bot/internal/lib/snapshot"
	"investudy_bot/internal/model"
	"investudy_bot/internal/pnl"
	"investudy_bot/internal/repository"
)

// readerStub подменяет репозиторий: сервис отвечает за выбор версии и за
// подготовку исключений, и проверять это удобнее без базы.
type readerStub struct {
	snapshots []model.Snapshot
	settings  model.ClosedReportsSettings
	rows      []model.ReportRow

	facts []pnl.Fact

	gotSnapshot int64
	gotCols     []period.Range
	gotDivision int32
	gotFrom     time.Time
	gotTo       time.Time
	gotExcluded []string
}

func (r *readerStub) ListSnapshots(context.Context, int) ([]model.Snapshot, error) {
	return r.snapshots, nil
}

func (r *readerStub) SnapshotByID(_ context.Context, id int64) (model.Snapshot, error) {
	for _, s := range r.snapshots {
		if s.ID == id {
			return s, nil
		}
	}

	return model.Snapshot{}, repository.ErrNotFound
}

func (r *readerStub) PnlStructure(context.Context) (pnl.Structure, error) {
	line := int32(1)

	return pnl.Structure{
		Lines: []pnl.Line{
			{ID: 1, Code: "1.1", Kind: pnl.Section, Sort: 10},
			{ID: 2, Code: "1", Kind: pnl.Total, Sort: 20},
			{ID: 3, Code: "2", Kind: pnl.Section, Sort: 30},
		},
		Items: map[string]*int32{"доходы": &line},
	}, nil
}

func (r *readerStub) PnlFacts(
	_ context.Context, snapshotID int64, cols []period.Range, divisionID int32,
) ([]pnl.Fact, error) {
	r.gotSnapshot, r.gotCols, r.gotDivision = snapshotID, cols, divisionID

	return r.facts, nil
}

func (r *readerStub) ClosedReportsSettings(context.Context) (model.ClosedReportsSettings, error) {
	return r.settings, nil
}

func (r *readerStub) ClosedReport(
	_ context.Context, snapshotID int64, from, to time.Time, excluded []string,
) ([]model.ReportRow, error) {
	r.gotSnapshot, r.gotFrom, r.gotTo, r.gotExcluded = snapshotID, from, to, excluded

	return r.rows, nil
}

func num(t *testing.T, s string) pgtype.Numeric {
	t.Helper()

	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("scan %q: %v", s, err)
	}

	return n
}

func snap(id int64, rows int64) model.Snapshot {
	return model.Snapshot{ID: id, RowCount: null.IntFrom(rows)}
}

func now() time.Time {
	return time.Date(2026, time.September, 2, 12, 0, 0, 0, time.FixedZone("Asia/Almaty", 5*60*60))
}

func TestClosedUsesLatestSnapshotAndPeriodBounds(t *testing.T) {
	reader := &readerStub{
		snapshots: []model.Snapshot{snap(142, 70000), snap(141, 69000)},
		settings:  model.ClosedReportsSettings{ExcludedItems: []string{"Пополнение", " перевод "}},
		rows: []model.ReportRow{
			{Division: "Алматы", Item: "Аренда", SubItem: "Офис", Debet: num(t, "1000.00"), Credit: num(t, "0.50")},
			{Division: "Астана", Item: "Аренда", SubItem: "Офис", Debet: num(t, "0.05"), Credit: num(t, "10.00")},
		},
	}

	rep, err := New(reader).Closed(context.Background(), period.PreviousMonth, now())
	if err != nil {
		t.Fatalf("Closed: %v", err)
	}

	if reader.gotSnapshot != 142 {
		t.Errorf("snapshot = %d, want 142", reader.gotSnapshot)
	}
	if rep.Stale {
		t.Error("отчёт по новейшему срезу помечен устаревшим")
	}
	if rep.Title != "Август 2026" {
		t.Errorf("Title = %q, want %q", rep.Title, "Август 2026")
	}

	const layout = "2006-01-02"
	if got := reader.gotFrom.Format(layout); got != "2026-08-01" {
		t.Errorf("from = %s, want 2026-08-01", got)
	}
	if got := reader.gotTo.Format(layout); got != "2026-09-01" {
		t.Errorf("to = %s, want 2026-09-01", got)
	}

	// Исключения обязаны доехать до запроса в нижнем регистре и без пробелов:
	// сравнение в SQL идёт через lower(), а лишний пробел его сорвёт.
	want := []string{"пополнение", "перевод"}
	if len(reader.gotExcluded) != len(want) {
		t.Fatalf("excluded = %v, want %v", reader.gotExcluded, want)
	}
	for i := range want {
		if reader.gotExcluded[i] != want[i] {
			t.Errorf("excluded[%d] = %q, want %q", i, reader.gotExcluded[i], want[i])
		}
	}

	if got := money.Format(rep.TotalDebet); got != "1 000,05" {
		t.Errorf("TotalDebet = %q, want %q", got, "1 000,05")
	}
	if got := money.Format(rep.TotalCredit); got != "10,50" {
		t.Errorf("TotalCredit = %q, want %q", got, "10,50")
	}
}

// Главный случай ради которого заведён snapshot.Latest: последний прогон
// парсера записал пустую версию, отчёт обязан показать предыдущую и сказать
// об этом.
func TestClosedFallsBackToPreviousSnapshot(t *testing.T) {
	reader := &readerStub{
		snapshots: []model.Snapshot{snap(143, 0), snap(142, 70000)},
	}

	rep, err := New(reader).Closed(context.Background(), period.CurrentMonth, now())
	if err != nil {
		t.Fatalf("Closed: %v", err)
	}

	if reader.gotSnapshot != 142 {
		t.Errorf("snapshot = %d, want 142", reader.gotSnapshot)
	}
	if !rep.Stale {
		t.Error("подмена версии не помечена: пользователь примет вчерашние данные за сегодняшние")
	}
}

func TestClosedWithoutUsableSnapshots(t *testing.T) {
	reader := &readerStub{snapshots: []model.Snapshot{snap(143, 0)}}

	_, err := New(reader).Closed(context.Background(), period.CurrentMonth, now())
	if !errors.Is(err, snapshot.ErrNoSnapshot) {
		t.Fatalf("err = %v, want ErrNoSnapshot", err)
	}
}

func TestClosedRejectsUnknownPeriod(t *testing.T) {
	reader := &readerStub{snapshots: []model.Snapshot{snap(142, 70000)}}

	if _, err := New(reader).Closed(context.Background(), "last_century", now()); err == nil {
		t.Fatal("неизвестный период принят молча")
	}
}

// Политика доезжает и до запроса (подразделение), и до движка (граница строк).
func TestPnLAppliesPolicy(t *testing.T) {
	reader := &readerStub{
		snapshots: []model.Snapshot{snap(142, 70000)},
		facts:     []pnl.Fact{{Col: 0, Item: "доходы", Sum: num(t, "10.00")}},
	}

	cols, err := period.ParseColumns(period.Month, []string{"2026-08"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := New(reader).PnL(context.Background(), access.Policy{LastLine: "1", DivisionID: 7}, 0, cols)
	if err != nil {
		t.Fatalf("PnL: %v", err)
	}

	if reader.gotSnapshot != 142 || reader.gotDivision != 7 {
		t.Errorf("запрос: срез %d, подразделение %d", reader.gotSnapshot, reader.gotDivision)
	}
	if len(reader.gotCols) != 1 || reader.gotCols[0].From.Format(time.DateOnly) != "2026-08-01" {
		t.Errorf("колонки: %+v", reader.gotCols)
	}

	if n := len(got.Report.Lines); n != 2 || got.Report.Lines[n-1].Code != "1" {
		t.Errorf("граница «1» не применена: %+v", got.Report.Lines)
	}
}

// Явно выбранная старая версия читается и помечается как не новейшая;
// несуществующая и пустая — ErrSnapshotNotFound.
func TestPnLExplicitSnapshot(t *testing.T) {
	reader := &readerStub{snapshots: []model.Snapshot{snap(143, 70000), snap(142, 0), snap(141, 69000)}}
	svc := New(reader)
	cols := period.DefaultColumns(now())

	got, err := svc.PnL(context.Background(), access.Policy{}, 141, cols)
	if err != nil {
		t.Fatalf("PnL: %v", err)
	}
	if got.Snapshot.ID != 141 || !got.Stale {
		t.Errorf("срез %d stale=%v, ждали 141 и stale", got.Snapshot.ID, got.Stale)
	}

	for _, id := range []int64{142, 999} {
		if _, err = svc.PnL(context.Background(), access.Policy{}, id, cols); !errors.Is(err, ErrSnapshotNotFound) {
			t.Errorf("срез %d: %v, ждали ErrSnapshotNotFound", id, err)
		}
	}
}
