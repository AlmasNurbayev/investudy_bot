package sheets

import (
	"errors"
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

	// Ячейка, отформатированная как дата со временем, приходит с хвостом. Разные
	// часы в пределах суток обязаны давать один и тот же день, иначе отбор по
	// period расщепился бы на несколько значений одного месяца.
	for _, in := range []string{
		"01.08.2026",
		"01.08.2026 00:00:00",
		"01.08.2026 12:00:00",
		"01.08.2026\u00a012:00", // неразрывный пробел
	} {
		got, err = parseDate(in)
		if err != nil {
			t.Fatalf("parseDate(%q): unexpected error: %v", in, err)
		}

		want = time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
		if !got.Valid || !got.Time.Equal(want) {
			t.Fatalf("parseDate(%q) = %+v, want %v", in, got, want)
		}
	}
}

// Google Sheets обрезает хвостовые пустые ячейки, поэтому строка приходит короче
// объявленных 24 колонок — недостающие поля должны стать NULL, а не паникой.
func TestParseRowTruncated(t *testing.T) {
	raw := []any{"05.03.2026", "  177  ", "перевод", "", "", "", "46829,00"}

	row, re := parseRow(raw)
	if re != nil {
		t.Fatalf("unexpected error: %v", re)
	}

	if row.NumOper != null.StringFrom("177") {
		t.Errorf("NumOper = %+v, want %q (пробелы обрезаются)", row.NumOper, "177")
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

	// Пустая текстовая ячейка — NULL, а не пустая строка: иначе бот искал бы
	// в запросах и IS NULL, и = ''.
	if row.Sender.Valid {
		t.Errorf("Sender = %+v, want NULL (пустая ячейка)", row.Sender)
	}
	if row.Comment1.Valid {
		t.Errorf("Comment1 = %+v, want NULL (колонка за концом строки)", row.Comment1)
	}
}

func TestParseRowReportsColumn(t *testing.T) {
	raw := make([]any, colSumCost+1)
	raw[colDate] = "05.03.2026"
	raw[colSumCost] = "битое"

	_, re := parseRow(raw)
	if re == nil {
		t.Fatal("expected error")
	}
	if re.Column != "СуммаРасход" {
		t.Errorf("Column = %q, want %q", re.Column, "СуммаРасход")
	}
	if re.Value != "битое" {
		t.Errorf("Value = %q, want %q", re.Value, "битое")
	}
	if re.Want != wantNumber {
		t.Errorf("Want = %q, want %q", re.Want, wantNumber)
	}
}

// Оповещение администратора должно указывать на строку в листе: ошибку он идёт
// чинить в Google Sheets, а не в логе.
func TestParseRowsErrorCarriesRowContext(t *testing.T) {
	raw := make([]any, colSumCost+1)
	raw[colDate] = "05.03.2026"
	raw[colBank] = "Kaspi"
	raw[colOrganization] = "Aligee"
	raw[colSumCost] = "12 000 тг"

	values := [][]any{
		{"04.03.2026", "176"}, // строка 2 листа
		raw,                   // строка 3
	}

	_, _, err := parseRows(values, parseOpts{sheet: "ДДС"})

	var re *RowError
	if !errors.As(err, &re) {
		t.Fatalf("error %v is not a *RowError", err)
	}

	if re.Row != 3 {
		t.Errorf("Row = %d, want 3", re.Row)
	}
	if re.Cell != "W3" {
		t.Errorf("Cell = %q, want %q", re.Cell, "W3")
	}
	if re.Sheet != "ДДС" || re.Bank != "Kaspi" || re.Organization != "Aligee" {
		t.Errorf("sheet/bank/org = %q/%q/%q", re.Sheet, re.Bank, re.Organization)
	}
	if !strings.Contains(re.Error(), "W3") {
		t.Errorf("Error() = %q, want the cell address in it", re.Error())
	}
}

// Отсечка по периоду срабатывает до разбора остальных колонок: строка, которую
// мы не грузим, не должна ронять синк своей испорченной суммой.
func TestParseRowsMinPeriod(t *testing.T) {
	row := func(date, period, sum string) []any {
		r := make([]any, colSumCost+1)
		r[colDate], r[colPeriod], r[colSumCost] = date, period, sum
		return r
	}

	values := [][]any{
		row("14.07.2019", "01.07.2019", "мусор из старых лет"),
		row("05.03.2026", "01.03.2026", "1000,00"),
		row("06.03.2026", "", "2000,00"), // период не заполнен
	}

	opts := parseOpts{
		sheet:     "ДДС",
		minPeriod: time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC),
	}

	rows, st, err := parseRows(values, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if st.beforeMinPeriod != 1 {
		t.Errorf("beforeMinPeriod = %d, want 1", st.beforeMinPeriod)
	}

	// Строка без периода остаётся: доказать, что она старая, нечем.
	if len(rows) != 2 {
		t.Fatalf("parsed %d rows, want 2", len(rows))
	}

	// Без отсечки та же строка обязана уронить синк — иначе проводка пропала бы
	// из отчёта незаметно.
	if _, _, err = parseRows(values, parseOpts{sheet: "ДДС"}); err == nil {
		t.Fatal("without MIN_PERIOD: expected error on the broken 2019 row")
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

	rows, st, err := parseRows(values, parseOpts{sheet: "ДДС"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rows) != 2 {
		t.Fatalf("parsed %d rows, want 2", len(rows))
	}
	if st.noDate != 3 {
		t.Errorf("noDate = %d, want 3", st.noDate)
	}
	if rows[0].NumOper.String != "177" || rows[1].NumOper.String != "178" {
		t.Errorf("kept rows %+v and %+v, want 177 and 178", rows[0].NumOper, rows[1].NumOper)
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

	rows, _, err := parseRows(values, parseOpts{sheet: "ДДС"})
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

	if _, _, err := parseRows(values, parseOpts{sheet: "ДДС"}); err == nil {
		t.Fatal("expected error for a dated row with a broken amount")
	}
}
