package parser

import (
	"testing"
	"time"

	"github.com/guregu/null/v6"

	"investudy_bot/internal/model"
)

// Обязательный минимум аналитики — подразделение, статья и период: по ним
// строятся все разрезы отчёта, и без любого из них проводка в отчёте невидима.
func TestCountGaps(t *testing.T) {
	period := null.TimeFrom(time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC))

	rows := []model.Row{
		{Division: "Отдел продаж", Item: "Аренда", Period: period},
		{Item: "Аренда", Period: period},           // без подразделения
		{Division: "Отдел продаж", Period: period}, // без статьи
		{Division: "Отдел продаж", Item: "Аренда"}, // без периода
		{}, // пусто всё сразу
	}

	got := countGaps(rows)

	// Rows меньше суммы остальных счётчиков: в последней строке пусто всё сразу,
	// а строк без аналитики она всё равно одна.
	want := Gaps{Rows: 4, NoDivision: 2, NoItem: 2, NoPeriod: 2}
	if got != want {
		t.Fatalf("countGaps = %+v, want %+v", got, want)
	}
}
