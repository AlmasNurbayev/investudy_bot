package sheets

import (
	"strings"
	"testing"
	"time"

	"github.com/guregu/null/v6"
)

func TestParseNum(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want null.Float
		err  bool
	}{
		{name: "empty is NULL", in: "", want: null.Float{}},
		{name: "comma as decimal separator", in: "46829,00", want: null.FloatFrom(46829)},
		{name: "space as thousands separator", in: "1 046 829,50", want: null.FloatFrom(1046829.5)},
		{name: "non-breaking space", in: "46 829,00", want: null.FloatFrom(46829)},
		{name: "narrow non-breaking space", in: "46 829,00", want: null.FloatFrom(46829)},
		{name: "negative", in: "-1500,25", want: null.FloatFrom(-1500.25)},
		{name: "plain integer", in: "42", want: null.FloatFrom(42)},
		{name: "dot as decimal separator", in: "3.14", want: null.FloatFrom(3.14)},
		{name: "zero is not NULL", in: "0", want: null.FloatFrom(0)},
		{name: "not a number", in: "н/д", err: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseNum(c.in)

			if c.err {
				if err == nil {
					t.Fatalf("parseNum(%q): expected error, got %v", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseNum(%q): unexpected error: %v", c.in, err)
			}

			if got != c.want {
				t.Fatalf("parseNum(%q) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

func TestParseDate(t *testing.T) {
	got, err := parseDate("05.03.2026")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, time.March, 5, 0, 0, 0, 0, time.UTC)
	if !got.Valid || !got.Time.Equal(want) {
		t.Fatalf("parseDate = %+v, want %v", got, want)
	}

	if got, err = parseDate(""); err != nil || got.Valid {
		t.Fatalf("parseDate(\"\") = %+v, %v; want invalid, nil", got, err)
	}

	if _, err = parseDate("2026-03-05"); err == nil {
		t.Fatal("parseDate(ISO format): expected error")
	}
}

// Google Sheets обрезает хвостовые пустые ячейки, поэтому строка приходит короче
// объявленных 24 колонок — недостающие поля должны стать NULL, а не паникой.
func TestParseRowTruncated(t *testing.T) {
	raw := []any{"05.03.2026", "  177  ", "перевод", "", "", "", "46829,00"}

	row, err := parseRow(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if row.NumOper != "177" {
		t.Errorf("NumOper = %q, want %q (пробелы обрезаются)", row.NumOper, "177")
	}
	if row.Debet != null.FloatFrom(46829) {
		t.Errorf("Debet = %+v, want 46829", row.Debet)
	}
	if row.Credit.Valid {
		t.Errorf("Credit = %+v, want NULL (взаимоисключающая с Debet)", row.Credit)
	}
	if row.SumReturn.Valid {
		t.Errorf("SumReturn = %+v, want NULL (колонки за концом строки)", row.SumReturn)
	}
	if row.Vid != "" {
		t.Errorf("Vid = %q, want empty", row.Vid)
	}
}

func TestParseRowReportsColumn(t *testing.T) {
	raw := make([]any, colSumCost+1)
	raw[colDate] = "05.03.2026"
	raw[colSumCost] = "битое"

	_, err := parseRow(raw)
	if err == nil {
		t.Fatal("expected error")
	}
	if want := "sum_cost"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not name the column %q", err, want)
	}
}

// Действительность строки определяет заполненная дата.
func TestParseRowsSkipsRowsWithoutDate(t *testing.T) {
	values := [][]any{
		{"05.03.2026", "177", "перевод", "", "", "", "46829,00"},
		{},              // пустая строка
		{"", "  ", nil}, // разделитель из пробелов
		{"", "ИТОГО", "", "", "", "", "не число"}, // итоговая строка: без даты, с мусором в сумме
		{"06.03.2026", "178"},
	}

	rows, skipped, err := parseRows(values)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rows) != 2 {
		t.Fatalf("parsed %d rows, want 2", len(rows))
	}
	if skipped != 3 {
		t.Errorf("skipped = %d, want 3", skipped)
	}
	if rows[0].NumOper != "177" || rows[1].NumOper != "178" {
		t.Errorf("kept rows %q and %q, want 177 and 178", rows[0].NumOper, rows[1].NumOper)
	}
}

func TestDecimals(t *testing.T) {
	cases := map[string]int{
		"":          0,
		"42":        0,
		"17,33":     2,
		"17.33333":  5,
		"46 829,00": 2,
		"0,0021834": 7,
	}

	for in, want := range cases {
		if got := decimals(in); got != want {
			t.Errorf("decimals(%q) = %d, want %d", in, got, want)
		}
	}
}

// Длинную дробь в суммах округляет Postgres при записи в NUMERIC(17,2); парсер
// её не трогает, но обязан донести значение без потерь — иначе округление
// произошло бы дважды, на float64 и в базе, и второе получило бы неверный вход.
func TestParseRowsKeepsLongFractions(t *testing.T) {
	values := [][]any{
		{"05.03.2026", "177", "перевод", "", "", "", "17,33333"},
	}

	rows, _, err := parseRows(values)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("parsed %d rows, want 1", len(rows))
	}
	if got := rows[0].Debet; !got.Valid || got.Float64 != 17.33333 {
		t.Errorf("Debet = %+v, want 17.33333 без округления в Go", got)
	}
}

// Строка с датой обязана разбираться целиком: тихо проглотить проводку с битой
// суммой хуже, чем упасть, — она пропала бы из отчёта незаметно.
func TestParseRowsFailsOnBrokenRowWithDate(t *testing.T) {
	values := [][]any{
		{"05.03.2026", "177", "перевод", "", "", "", "не число"},
	}

	if _, _, err := parseRows(values); err == nil {
		t.Fatal("expected error for a dated row with a broken amount")
	}
}
