package period

import (
	"strings"
	"testing"
	"time"
)

func TestParseColumns(t *testing.T) {
	cases := []struct {
		grain Grain
		key   string
		want  string // key from to title
	}{
		{Month, "2026-03", "2026-03 2026-03-01 2026-04-01 Мар 2026"},
		{Month, "2025-12", "2025-12 2025-12-01 2026-01-01 Дек 2025"},
		{Quarter, "2026-q4", "2026-Q4 2026-10-01 2027-01-01 IV кв. 2026"},
		{Year, "2025", "2025 2025-01-01 2026-01-01 2025"},
	}

	for _, c := range cases {
		cols, err := ParseColumns(c.grain, []string{c.key})
		if err != nil {
			t.Errorf("%s %s: %v", c.grain, c.key, err)
			continue
		}

		col := cols[0]
		got := strings.Join([]string{col.Key, col.From.Format(time.DateOnly), col.To.Format(time.DateOnly), col.Title}, " ")
		if got != c.want {
			t.Errorf("%s %s = %q, ждали %q", c.grain, c.key, got, c.want)
		}
	}
}

func TestParseColumnsRejects(t *testing.T) {
	many := make([]string, MaxColumns+1)
	for i := range many {
		many[i] = time.Date(2020, time.Month(i+1), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
	}

	cases := map[string]struct {
		grain Grain
		keys  []string
	}{
		"пусто":     {Month, nil},
		"больше 12": {Month, many},
		"повтор":    {Month, []string{"2026-03", "2026-03"}},
		"квартал среди месяцев": {Month, []string{"2026-03", "2026-Q1"}},
		"месяц 13":           {Month, []string{"2026-13"}},
		"месяц одной цифрой": {Month, []string{"2026-3"}},
		"знак в месяце":      {Month, []string{"2026-+3"}},
		"квартал 5":          {Quarter, []string{"2026-Q5"}},
		"год из букв":        {Year, []string{"abcd"}},
		"неизвестная длина":  {"week", []string{"2026"}},
	}

	for name, c := range cases {
		if _, err := ParseColumns(c.grain, c.keys); err == nil {
			t.Errorf("%s: ошибки нет", name)
		}
	}
}

// По умолчанию — шесть месяцев по текущий, через границу года.
func TestDefaultColumns(t *testing.T) {
	cols := DefaultColumns(time.Date(2026, time.March, 31, 23, 0, 0, 0, time.UTC))

	keys := make([]string, len(cols))
	for i, c := range cols {
		keys[i] = c.Key
	}

	if got := strings.Join(keys, ","); got != "2025-10,2025-11,2025-12,2026-01,2026-02,2026-03" {
		t.Errorf("по умолчанию: %s", got)
	}
}
