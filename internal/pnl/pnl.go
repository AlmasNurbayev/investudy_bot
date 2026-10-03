// Package pnl — движок ОПиУ: структура отчёта + агрегаты среза → дерево строк
// с итогами.
//
// Один на сайт и бота: два разных ответа на вопрос «сколько мы заработали»
// хуже, чем отсутствие второго канала. Движок — чистая функция над срезом
// строк, базы не знает: сколько и каких агрегатов прочитать, решает
// репозиторий, кому что показывать — internal/access.
package pnl

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"investudy_bot/internal/lib/money"
)

// Kind — тип строки отчёта.
type Kind string

const (
	// Section — сумма проводок по статьям, привязанным к строке.
	Section Kind = "section"
	// Total — нарастающая сумма всех разделов выше. Формулы у итога нет,
	// поэтому «забыть» раздел, как Gross Profit в листе, он не может.
	Total Kind = "total"
)

// Dim — разрез раскрытия раздела.
type Dim string

const (
	ByDivision Dim = "division"
	BySubItem  Dim = "sub_item"
)

// Line — строка отчёта (таблица pnl_lines).
type Line struct {
	ID    int32
	Code  string
	Title string
	Kind  Kind
	// Sort — порядок сверху вниз. Граница доступа сравнивается по нему,
	// а не по коду: коды лексикографически не упорядочены.
	Sort   int
	Expand []Dim
}

// Structure — строки отчёта и разметка статей.
type Structure struct {
	Lines []Line
	// Items — разметка статей (таблица pnl_item_map): имя статьи → id раздела.
	// nil — статья исключена намеренно; статьи нет в карте — она не размечена.
	Items map[string]*int32
}

// Ref — значение справочника: подразделение или подстатья.
// ID = 0 — значение не заполнено в листе, Name тогда пустое.
type Ref struct {
	ID   int32
	Name string
}

// Fact — агрегат среза: сумма sum_dash одной колонки по тройке
// «статья / подразделение / подстатья».
type Fact struct {
	Col      int
	Item     string
	Division Ref
	SubItem  Ref
	Sum      pgtype.Numeric
}

// Report — посчитанный отчёт.
type Report struct {
	Lines []LineValues
	// Unmapped — статьи, о которых структура ничего не знает. Отдельным блоком,
	// а не молча мимо: новая статья из листа иначе выпала бы из отчёта
	// с заполненной суммой, и итог просто перестал бы сходиться.
	Unmapped []Unmapped
}

// LineValues — строка отчёта с суммами по колонкам.
type LineValues struct {
	Line
	Values   []pgtype.Numeric
	Children []Node
}

// Node — узел раскрытия раздела.
type Node struct {
	Dim Dim
	Ref
	Values   []pgtype.Numeric
	Children []Node
}

// Unmapped — неразмеченная статья с суммами по колонкам.
// Item = "" — проводки без статьи вовсе.
type Unmapped struct {
	Item   string
	Values []pgtype.Numeric
}

// NormalizeItem приводит имя статьи к виду ключа разметки.
//
// В Go, а не только CHECK-ом в базе: сравнение должно давать один ответ
// независимо от локали Postgres, а lower() с C-локалью кириллицу не трогает.
func NormalizeItem(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// Build считает отчёт на cols колонок.
//
// lastLine — код последней видимой строки, включительно; "" — все строки.
// Строки ниже границы считаются (итоги нарастающие, им нужны все разделы
// выше) и только потом отрезаются.
func Build(s Structure, cols int, facts []Fact, lastLine string) (Report, error) {
	lines, err := s.Visible(lastLine)
	if err != nil {
		return Report{}, err
	}

	sections := make(map[int32]*tree, len(lines))
	for _, l := range lines {
		if l.Kind == Section {
			sections[l.ID] = &tree{dims: l.Expand, values: zeros(cols)}
		}
	}

	items := make(map[string]*int32, len(s.Items))
	for name, line := range s.Items {
		items[NormalizeItem(name)] = line
	}

	unmapped := map[string][]pgtype.Numeric{}

	for _, f := range facts {
		if f.Col < 0 || f.Col >= cols {
			return Report{}, fmt.Errorf("агрегат статьи %q вне колонок: %d из %d", f.Item, f.Col, cols)
		}

		item := NormalizeItem(f.Item)

		line, mapped := items[item]
		switch {
		case !mapped:
			if unmapped[item] == nil {
				unmapped[item] = zeros(cols)
			}
			add(unmapped[item], f.Col, f.Sum)

		case line == nil:
			// Исключена намеренно: движение денег в ОПиУ не входит.

		default:
			// Раздел за границей доступа в sections не попал: его сумма
			// не нужна ни строке, ни итогам до границы.
			if t := sections[*line]; t != nil {
				t.add(f)
			}
		}
	}

	out := Report{Lines: make([]LineValues, 0, len(lines))}

	running := zeros(cols)
	for _, l := range lines {
		lv := LineValues{Line: l}

		switch l.Kind {
		case Section:
			t := sections[l.ID]
			lv.Values, lv.Children = t.values, t.nodes()

			for i := range running {
				running[i] = money.Add(running[i], t.values[i])
			}

		case Total:
			lv.Values = slices.Clone(running)
		}

		out.Lines = append(out.Lines, lv)
	}

	for item, values := range unmapped {
		out.Unmapped = append(out.Unmapped, Unmapped{Item: item, Values: values})
	}
	// Без статьи — последним: это не статья, а пропуск в листе.
	slices.SortFunc(out.Unmapped, func(a, b Unmapped) int {
		return cmp.Or(emptyLast(a.Item == "", b.Item == ""), cmp.Compare(a.Item, b.Item))
	})

	return out, nil
}

// Visible отдаёт строки сверху вниз до lastLine включительно ("" — все)
// и проверяет структуру.
//
// Ошибки структуры — громкие: отчёт по сломанной разметке выглядел бы
// исправным, а цена — неверная прибыль или чужие строки за границей роли.
func (s Structure) Visible(lastLine string) ([]Line, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}

	lines := slices.Clone(s.Lines)
	slices.SortFunc(lines, func(a, b Line) int { return cmp.Compare(a.Sort, b.Sort) })

	if lastLine == "" {
		return lines, nil
	}

	for i, l := range lines {
		if l.Code == lastLine {
			return lines[:i+1], nil
		}
	}

	// Не «показать всё»: граница, которой нет в структуре, — это ошибка
	// политики или правки структуры, и молча раскрыть отчёт целиком нельзя.
	return nil, fmt.Errorf("граница доступа: строки %q нет в структуре ОПиУ", lastLine)
}

// Validate проверяет структуру ОПиУ.
//
// В Go, а не ограничениями базы: так решено для проекта целиком — в базе
// только ключи, UNIQUE и NOT NULL. Поэтому проверка обязана стоять на обоих
// путях: при сохранении из админки и при каждом расчёте (Build → Visible),
// чтобы правка руками в базе не дала тихо неверный отчёт.
func (s Structure) Validate() error {
	kinds := make(map[int32]Kind, len(s.Lines))

	for _, l := range s.Lines {
		switch l.Kind {
		case Section:
			seen := map[Dim]bool{}
			for _, d := range l.Expand {
				if d != ByDivision && d != BySubItem {
					return fmt.Errorf("строка %s: неизвестный разрез раскрытия %q", l.Code, d)
				}
				if seen[d] {
					return fmt.Errorf("строка %s: разрез %q в раскрытии дважды", l.Code, d)
				}
				seen[d] = true
			}

		case Total:
			if len(l.Expand) > 0 {
				return fmt.Errorf("строка %s: у итога не бывает раскрытия", l.Code)
			}

		default:
			return fmt.Errorf("строка %s: неизвестный тип %q", l.Code, l.Kind)
		}

		kinds[l.ID] = l.Kind
	}

	for name, line := range s.Items {
		if NormalizeItem(name) == "" {
			return fmt.Errorf("в разметке статья с пустым именем")
		}

		if line != nil && kinds[*line] != Section {
			return fmt.Errorf("статья %q привязана к строке %d, а это не раздел", name, *line)
		}
	}

	return nil
}

// tree копит раскрытие одного раздела.
type tree struct {
	dims     []Dim
	values   []pgtype.Numeric
	children map[Ref]*treeNode
}

type treeNode struct {
	dim Dim
	ref Ref
	tree
}

func (t *tree) add(f Fact) {
	add(t.values, f.Col, f.Sum)

	if len(t.dims) == 0 {
		return
	}

	dim := t.dims[0]
	ref := f.Division
	if dim == BySubItem {
		ref = f.SubItem
	}

	if t.children == nil {
		t.children = map[Ref]*treeNode{}
	}

	child := t.children[ref]
	if child == nil {
		child = &treeNode{dim: dim, ref: ref, tree: tree{dims: t.dims[1:], values: zeros(len(t.values))}}
		t.children[ref] = child
	}

	child.add(f)
}

// nodes отдаёт раскрытие по имени; незаполненное значение — последним.
func (t *tree) nodes() []Node {
	if len(t.children) == 0 {
		return nil
	}

	out := make([]Node, 0, len(t.children))
	for _, c := range t.children {
		out = append(out, Node{Dim: c.dim, Ref: c.ref, Values: c.values, Children: c.nodes()})
	}

	slices.SortFunc(out, func(a, b Node) int {
		return cmp.Or(
			emptyLast(a.ID == 0, b.ID == 0),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.ID, b.ID),
		)
	})

	return out
}

// emptyLast — порядок, ставящий пустое значение после заполненных.
func emptyLast(aEmpty, bEmpty bool) int {
	switch {
	case aEmpty == bEmpty:
		return 0
	case aEmpty:
		return 1
	}

	return -1
}

func zeros(n int) []pgtype.Numeric {
	out := make([]pgtype.Numeric, n)
	for i := range out {
		out[i] = money.Sum(nil)
	}

	return out
}

func add(values []pgtype.Numeric, col int, sum pgtype.Numeric) {
	values[col] = money.Add(values[col], sum)
}
