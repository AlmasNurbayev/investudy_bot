package money

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func num(t *testing.T, s string) pgtype.Numeric {
	t.Helper()

	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("scan %q: %v", s, err)
	}

	return n
}

func TestFormat(t *testing.T) {
	cases := map[string]string{
		"0":           "0,00",
		"0.00":        "0,00",
		"45.5":        "45,50",
		"-45.5":       "-45,50",
		"999":         "999,00",
		"1000":        "1 000,00",
		"1234567.89":  "1 234 567,89",
		"-1234567.89": "-1 234 567,89",
	}

	for in, want := range cases {
		if got := Format(num(t, in)); got != want {
			t.Errorf("Format(%s) = %q, want %q", in, got, want)
		}
	}
}

// NULL приходит из левых джойнов и из пустых агрегатов; печатать «0,00» на этом
// месте нельзя — ноль и «не считалось» в отчёте означают разное.
func TestFormatNull(t *testing.T) {
	if got := Format(pgtype.Numeric{}); got != "—" {
		t.Errorf("Format(NULL) = %q, want %q", got, "—")
	}
}

// Сложение — единственное место, где точность NUMERIC могла бы потеряться:
// вход с разным масштабом обязан сложиться без округлений.
func TestSum(t *testing.T) {
	cases := map[string]struct {
		in   []string
		want string
	}{
		"разный масштаб":   {[]string{"1000", "0.05", "12.30"}, "1 012,35"},
		"плюс и минус":     {[]string{"100.00", "-40.50"}, "59,50"},
		"копейки не тонут": {[]string{"0.01", "0.01", "0.01"}, "0,03"},
	}

	for name, c := range cases {
		values := make([]pgtype.Numeric, len(c.in))
		for i, s := range c.in {
			values[i] = num(t, s)
		}

		if got := Format(Sum(values)); got != c.want {
			t.Errorf("%s: Sum = %q, want %q", name, got, c.want)
		}
	}
}

// Пустой отчёт даёт «0,00», а не прочерк: строк нет — это ноль, а не «неизвестно».
func TestSumOfNothingIsZero(t *testing.T) {
	if got := Format(Sum(nil)); got != "0,00" {
		t.Errorf("Sum(nil) = %q, want %q", got, "0,00")
	}
}

// NULL приходит из левых джойнов и не должен обнулять весь итог.
func TestSumSkipsNull(t *testing.T) {
	if got := Format(Sum([]pgtype.Numeric{num(t, "10.00"), {}, num(t, "5.00")})); got != "15,00" {
		t.Errorf("Sum = %q, want %q", got, "15,00")
	}
}

// Суммы могут быть большими: 70 тыс. проводок по миллиону — это уже за
// пределами точного диапазона float64, и именно ради этого случая сложение
// идёт через big.Int.
func TestSumStaysExactOnLargeValues(t *testing.T) {
	values := make([]pgtype.Numeric, 0, 3)
	for range 3 {
		values = append(values, num(t, "99999999999999.99"))
	}

	if got := Format(Sum(values)); got != "299 999 999 999 999,97" {
		t.Errorf("Sum = %q, want %q", got, "299 999 999 999 999,97")
	}
}

// Decimal уходит в JSON: точка, ровно два знака, знак минус, без разрядов.
func TestDecimal(t *testing.T) {
	cases := map[string]string{
		"0":                 "0.00",
		"45.5":              "45.50",
		"-1234567.89":       "-1234567.89",
		"99999999999999.99": "99999999999999.99",
	}

	for in, want := range cases {
		if got := Decimal(num(t, in)); got != want {
			t.Errorf("Decimal(%s) = %q, want %q", in, got, want)
		}
	}

	if got := Decimal(pgtype.Numeric{}); got != "" {
		t.Errorf("Decimal(NULL) = %q, want empty", got)
	}
}

func TestAdd(t *testing.T) {
	if got := Decimal(Add(num(t, "100.10"), num(t, "-0.15"))); got != "99.95" {
		t.Errorf("Add = %q, want %q", got, "99.95")
	}

	if got := Decimal(Add(pgtype.Numeric{}, num(t, "1"))); got != "1.00" {
		t.Errorf("Add(NULL, 1) = %q, want %q", got, "1.00")
	}
}
