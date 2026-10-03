package pnl

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"investudy_bot/internal/lib/money"
)

func num(t *testing.T, s string) pgtype.Numeric {
	t.Helper()

	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("scan %q: %v", s, err)
	}

	return n
}

func id(v int32) *int32 { return &v }

// structure — урезанная копия сида 000003: два раздела под первым итогом,
// раздел без раскрытия под вторым, исключённая статья.
func structure() Structure {
	return Structure{
		Lines: []Line{
			// Порядок перемешан намеренно: движок обязан сортировать по Sort сам.
			{ID: 4, Code: "4", Title: "Операционная прибыль", Kind: Total, Sort: 70},
			{ID: 1, Code: "1.1", Title: "Выручка", Kind: Section, Sort: 10, Expand: []Dim{ByDivision}},
			{ID: 2, Code: "1.3", Title: "Расходы", Kind: Section, Sort: 30, Expand: []Dim{ByDivision, BySubItem}},
			{ID: 3, Code: "1", Title: "Валовая прибыль", Kind: Total, Sort: 40},
			{ID: 5, Code: "2", Title: "Админ", Kind: Section, Sort: 50},
		},
		Items: map[string]*int32{
			"доходы":          id(1),
			"произв. расходы": id(2),
			"админ. расходы":  id(5),
			"перевод":         nil,
		},
	}
}

var (
	sales = Ref{ID: 10, Name: "отдел продаж"}
	vi    = Ref{ID: 11, Name: "ВИ"}
	rent  = Ref{ID: 20, Name: "аренда"}
	wages = Ref{ID: 21, Name: "оплата труда"}
)

func facts(t *testing.T) []Fact {
	return []Fact{
		{Col: 0, Item: "доходы", Division: sales, Sum: num(t, "1000.00")},
		{Col: 0, Item: "доходы", Division: vi, Sum: num(t, "500.50")},
		{Col: 1, Item: "Доходы ", Division: sales, Sum: num(t, "2000.00")},
		{Col: 0, Item: "произв. расходы", Division: sales, SubItem: rent, Sum: num(t, "-300.00")},
		{Col: 0, Item: "произв. расходы", Division: sales, SubItem: wages, Sum: num(t, "-100.25")},
		{Col: 1, Item: "произв. расходы", SubItem: rent, Sum: num(t, "-50.00")},
		{Col: 0, Item: "админ. расходы", Sum: num(t, "-10.00")},
		{Col: 0, Item: "перевод", Division: sales, Sum: num(t, "999999.00")},
		{Col: 1, Item: "новая статья", Sum: num(t, "7.00")},
		{Col: 0, Item: "", Sum: num(t, "3.00")},
	}
}

func values(vs []pgtype.Numeric) string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = money.Decimal(v)
	}

	return strings.Join(out, " | ")
}

func build(t *testing.T, lastLine string) Report {
	t.Helper()

	r, err := Build(structure(), 2, facts(t), lastLine)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	return r
}

// Разделы — суммы своих статей, итоги — нарастающие суммы всех разделов
// выше. Исключённая статья не влияет ни на что.
func TestBuildLinesAndTotals(t *testing.T) {
	r := build(t, "")

	want := []struct{ code, values string }{
		{"1.1", "1500.50 | 2000.00"},
		{"1.3", "-400.25 | -50.00"},
		{"1", "1100.25 | 1950.00"},
		{"2", "-10.00 | 0.00"},
		{"4", "1090.25 | 1950.00"},
	}

	if len(r.Lines) != len(want) {
		t.Fatalf("строк %d, ждали %d", len(r.Lines), len(want))
	}

	for i, w := range want {
		got := r.Lines[i]
		if got.Code != w.code || values(got.Values) != w.values {
			t.Errorf("строка %d: %s = %s, ждали %s = %s", i, got.Code, values(got.Values), w.code, w.values)
		}
	}
}

// Пустая колонка раздела — ноль, а не NULL: строк нет, значит, ноль.
func TestBuildEmptySectionIsZero(t *testing.T) {
	r, err := Build(structure(), 1, nil, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	for _, l := range r.Lines {
		if got := values(l.Values); got != "0.00" {
			t.Errorf("%s = %q, ждали 0.00", l.Code, got)
		}
	}
}

// Раскрытие идёт по пути expand; незаполненное подразделение — последним.
func TestBuildExpand(t *testing.T) {
	r := build(t, "")

	revenue := r.Lines[0]
	if len(revenue.Children) != 2 ||
		revenue.Children[0].Name != "ВИ" || revenue.Children[1].Name != "отдел продаж" {
		t.Fatalf("раскрытие выручки: %+v", revenue.Children)
	}
	if revenue.Children[0].Children != nil {
		t.Error("выручка раскрывается на один уровень, а пришло два")
	}

	costs := r.Lines[1]
	if len(costs.Children) != 2 {
		t.Fatalf("раскрытие расходов: %d подразделений, ждали 2", len(costs.Children))
	}

	byDiv := costs.Children[0]
	if byDiv.Dim != ByDivision || byDiv.Name != "отдел продаж" || values(byDiv.Values) != "-400.25 | 0.00" {
		t.Errorf("подразделение: %s %s %s", byDiv.Dim, byDiv.Name, values(byDiv.Values))
	}
	if len(byDiv.Children) != 2 || byDiv.Children[0].Dim != BySubItem || byDiv.Children[0].Name != "аренда" {
		t.Errorf("подстатьи отдела продаж: %+v", byDiv.Children)
	}

	none := costs.Children[1]
	if none.ID != 0 || values(none.Values) != "0.00 | -50.00" {
		t.Errorf("строка без подразделения: id=%d %s", none.ID, values(none.Values))
	}

	if r.Lines[3].Children != nil {
		t.Error("раздел без expand раскрылся")
	}
}

// Неразмеченная статья не пропадает молча; проводки без статьи — последними.
// Исключённая в неразмеченные не попадает.
func TestBuildUnmapped(t *testing.T) {
	r := build(t, "")

	if len(r.Unmapped) != 2 {
		t.Fatalf("неразмеченных %d, ждали 2: %+v", len(r.Unmapped), r.Unmapped)
	}

	if r.Unmapped[0].Item != "новая статья" || values(r.Unmapped[0].Values) != "0.00 | 7.00" {
		t.Errorf("первая: %q %s", r.Unmapped[0].Item, values(r.Unmapped[0].Values))
	}

	if r.Unmapped[1].Item != "" || values(r.Unmapped[1].Values) != "3.00 | 0.00" {
		t.Errorf("без статьи: %q %s", r.Unmapped[1].Item, values(r.Unmapped[1].Values))
	}
}

// Граница включительная и считается по Sort; итог до границы не меняется
// оттого, что строки ниже отрезаны.
func TestBuildCutoff(t *testing.T) {
	r := build(t, "1")

	if len(r.Lines) != 3 || r.Lines[2].Code != "1" {
		t.Fatalf("граница «1»: %d строк, последняя %q", len(r.Lines), r.Lines[len(r.Lines)-1].Code)
	}

	if got := values(r.Lines[2].Values); got != "1100.25 | 1950.00" {
		t.Errorf("валовая под границей = %s", got)
	}
}

func TestBuildErrors(t *testing.T) {
	cases := map[string]func() (Structure, []Fact, string){
		"граница вне структуры": func() (Structure, []Fact, string) {
			return structure(), nil, "6"
		},
		"статья привязана к итогу": func() (Structure, []Fact, string) {
			s := structure()
			s.Items["прочее"] = id(3)
			return s, nil, ""
		},
		"неизвестный тип строки": func() (Structure, []Fact, string) {
			s := structure()
			s.Lines[0].Kind = "subtotal"
			return s, nil, ""
		},
		"неизвестный разрез": func() (Structure, []Fact, string) {
			s := structure()
			s.Lines[1].Expand = []Dim{"divison"}
			return s, nil, ""
		},
		"разрез дважды": func() (Structure, []Fact, string) {
			s := structure()
			s.Lines[1].Expand = []Dim{ByDivision, ByDivision}
			return s, nil, ""
		},
		"раскрытие у итога": func() (Structure, []Fact, string) {
			s := structure()
			s.Lines[0].Expand = []Dim{ByDivision}
			return s, nil, ""
		},
		"статья с пустым именем": func() (Structure, []Fact, string) {
			s := structure()
			s.Items[" "] = nil
			return s, nil, ""
		},
		"агрегат вне колонок": func() (Structure, []Fact, string) {
			return structure(), []Fact{{Col: 2, Item: "доходы", Sum: num(t, "1")}}, ""
		},
	}

	for name, c := range cases {
		s, f, last := c()
		if _, err := Build(s, 2, f, last); err == nil {
			t.Errorf("%s: ошибки нет", name)
		}
	}
}
