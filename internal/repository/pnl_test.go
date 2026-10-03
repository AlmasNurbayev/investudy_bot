package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/guregu/null/v6"
	"github.com/jackc/pgx/v5"

	"investudy_bot/internal/lib/money"
	"investudy_bot/internal/lib/period"
	"investudy_bot/internal/model"
	"investudy_bot/internal/pnl"
)

func pnlRow(t *testing.T, period string, sum float64, division, item, subItem string) model.Row {
	t.Helper()

	return model.Row{
		Date:     date(t, period),
		Period:   date(t, period),
		SumDash:  null.FloatFrom(sum),
		Division: division,
		Item:     item,
		SubItem:  subItem,
	}
}

func pnlRows(t *testing.T) []model.Row {
	return []model.Row{
		pnlRow(t, "01.03.2026", 1000, "отдел продаж", "доходы", "курсы"),
		pnlRow(t, "01.03.2026", 250.5, "ВИ", "доходы", "курсы"),
		pnlRow(t, "01.04.2026", 2000, "отдел продаж", "доходы", "курсы"),
		pnlRow(t, "01.03.2026", -40, "отдел продаж", "возврат доходов", "возврат"),
		pnlRow(t, "01.03.2026", -300, "отдел продаж", "произв. расходы", "аренда"),
		pnlRow(t, "01.04.2026", -120.25, "", "произв. расходы", "аренда"),
		pnlRow(t, "01.03.2026", -70, "руководство", "админ. расходы", "аренда"),
		pnlRow(t, "01.04.2026", -30, "руководство", "общие налоги", "КПН"),
		pnlRow(t, "01.03.2026", 15, "руководство", "прочие доходы", "проценты"),
		pnlRow(t, "01.04.2026", -500, "руководство", "дивиденды", "учредитель"),
		// Размечена в сиде до первой проводки.
		pnlRow(t, "01.04.2026", -5, "руководство", "Сообщества расходы", "клуб"),
		// Исключена намеренно.
		pnlRow(t, "01.03.2026", 99999, "отдел продаж", "перевод", ""),
		// Не размечена.
		pnlRow(t, "01.03.2026", 7, "отдел продаж", "новая статья", ""),
		// Вне колонок отчёта.
		pnlRow(t, "01.02.2026", 123456, "отдел продаж", "доходы", "курсы"),
	}
}

func spring() []period.Range {
	return []period.Range{
		{From: time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)},
		{From: time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC)},
	}
}

// directSum — независимая сверка: sum(sum_dash) статей набора за колонку,
// прямым SQL мимо движка.
func directSum(t *testing.T, conn *pgx.Conn, snapshotID int64, col period.Range, lineCodes []string) string {
	t.Helper()

	const query = `
		SELECT coalesce(sum(d.sum_dash), 0)::numeric(17,2)::text
		FROM data d
		JOIN items it ON it.id = d.item_id
		JOIN pnl_item_map m ON m.item_name = lower(btrim(it.name))
		JOIN pnl_lines l ON l.id = m.line_id
		WHERE d.snapshot_id = $1 AND d.period >= $2 AND d.period < $3 AND l.code = ANY ($4)`

	var s string
	if err := conn.QueryRow(context.Background(), query, snapshotID, col.From, col.To, lineCodes).Scan(&s); err != nil {
		t.Fatalf("direct sum: %v", err)
	}

	return s
}

// Каждый раздел и итог совпадает с прямым sum(sum_dash) по статьям раздела,
// последний итог — с суммой всех размеченных разделов.
func TestPnlMatchesDirectSQL(t *testing.T) {
	reader, store, conn := newReader(t)
	ctx := context.Background()

	id, _ := publish(t, store, pnlRows(t))
	// Второй срез с другими суммами: отчёт обязан читать только свой.
	publish(t, store, []model.Row{pnlRow(t, "01.03.2026", 555, "отдел продаж", "доходы", "курсы")})

	structure, err := reader.PnlStructure(ctx)
	if err != nil {
		t.Fatalf("structure: %v", err)
	}

	cols := spring()

	facts, err := reader.PnlFacts(ctx, id, cols, 0)
	if err != nil {
		t.Fatalf("facts: %v", err)
	}

	report, err := pnl.Build(structure, len(cols), facts, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	var above []string
	for _, line := range report.Lines {
		codes := []string{line.Code}
		if line.Kind == pnl.Section {
			above = append(above, line.Code)
		} else {
			codes = above
		}

		for i, col := range cols {
			want := directSum(t, conn, id, col, codes)
			if got := money.Decimal(line.Values[i]); got != want {
				t.Errorf("%s, колонка %d: движок %s, SQL %s", line.Code, i, got, want)
			}
		}
	}

	if last := report.Lines[len(report.Lines)-1]; last.Code != "8" || money.Decimal(last.Values[1]) != "1344.75" {
		t.Errorf("нераспределённая за апрель: %s = %s", last.Code, money.Decimal(last.Values[1]))
	}

	if len(report.Unmapped) != 1 || report.Unmapped[0].Item != "новая статья" {
		t.Errorf("неразмеченные: %+v", report.Unmapped)
	}
}

// Статья, размеченная в сиде до первой проводки, попадает в свой раздел.
func TestPnlSeededItemLandsInSection(t *testing.T) {
	reader, store, _ := newReader(t)
	ctx := context.Background()

	id, _ := publish(t, store, pnlRows(t))

	structure, err := reader.PnlStructure(ctx)
	if err != nil {
		t.Fatalf("structure: %v", err)
	}

	facts, err := reader.PnlFacts(ctx, id, spring(), 0)
	if err != nil {
		t.Fatalf("facts: %v", err)
	}

	report, err := pnl.Build(structure, 2, facts, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	for _, line := range report.Lines {
		if line.Code == "7.5" && money.Decimal(line.Values[1]) != "-5.00" {
			t.Errorf("сообщества расходы = %s, ждали -5.00", money.Decimal(line.Values[1]))
		}
	}
}

// Фильтр руководителя — в запросе: чужих подразделений нет даже в агрегатах.
func TestPnlFactsDivisionFilter(t *testing.T) {
	reader, store, conn := newReader(t)
	ctx := context.Background()

	id, _ := publish(t, store, pnlRows(t))

	var sales int32
	if err := conn.QueryRow(ctx, `SELECT id FROM divisions WHERE name = 'отдел продаж'`).Scan(&sales); err != nil {
		t.Fatalf("division: %v", err)
	}

	facts, err := reader.PnlFacts(ctx, id, spring(), sales)
	if err != nil {
		t.Fatalf("facts: %v", err)
	}

	if len(facts) == 0 {
		t.Fatal("по своему подразделению ничего не пришло")
	}

	for _, f := range facts {
		if f.Division.ID != sales {
			t.Errorf("чужое подразделение в отчёте руководителя: %+v", f)
		}
	}
}
