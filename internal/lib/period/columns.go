package period

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Grain — длина колонки отчёта.
type Grain string

const (
	Month   Grain = "month"
	Quarter Grain = "quarter"
	Year    Grain = "year"
)

// MaxColumns — потолок колонок ОПиУ (решение 15 плана): шире таблица
// перестаёт читаться, а запрос — быть дешёвым.
const MaxColumns = 12

// DefaultMonths — сколько месяцев показывать, когда колонки не выбраны.
const DefaultMonths = 6

var shortMonths = [...]string{
	"Янв", "Фев", "Мар", "Апр", "Май", "Июн",
	"Июл", "Авг", "Сен", "Окт", "Ноя", "Дек",
}

// Column — колонка отчёта: ключ из URL и полуинтервал учётных периодов.
type Column struct {
	Key string
	Range
}

// ParseColumns разбирает ключи колонок одной гранулярности:
// месяц `2026-03`, квартал `2026-Q1`, год `2026`.
//
// Все колонки одной длины: сравнивать месяц с кварталом в одной таблице
// некорректно. Порядок сохраняется — его выбрал читатель; повтор — ошибка.
//
// Даты — полночь UTC: колонка period в базе — DATE, и зона тут только
// сдвинула бы день при передаче параметра.
func ParseColumns(g Grain, keys []string) ([]Column, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("не выбрано ни одной колонки")
	}
	if len(keys) > MaxColumns {
		return nil, fmt.Errorf("колонок %d, а можно не больше %d", len(keys), MaxColumns)
	}

	seen := map[string]bool{}
	out := make([]Column, 0, len(keys))

	for _, raw := range keys {
		key := strings.ToUpper(strings.TrimSpace(raw))
		if seen[key] {
			return nil, fmt.Errorf("колонка %s выбрана дважды", raw)
		}
		seen[key] = true

		c, err := parseColumn(g, key)
		if err != nil {
			return nil, err
		}

		out = append(out, c)
	}

	return out, nil
}

// DefaultColumns — последние DefaultMonths месяцев, включая месяц now.
func DefaultColumns(now time.Time) []Column {
	out := make([]Column, 0, DefaultMonths)

	for i := DefaultMonths - 1; i >= 0; i-- {
		// Отсчёт от первого числа: AddDate по произвольному дню
		// нормализует 31 марта минус месяц в 3 марта.
		from := time.Date(now.Year(), now.Month()-time.Month(i), 1, 0, 0, 0, 0, time.UTC)
		out = append(out, monthColumn(from))
	}

	return out
}

func parseColumn(g Grain, key string) (Column, error) {
	bad := func() (Column, error) {
		return Column{}, fmt.Errorf("колонка %q не похожа на %s", key, example[g])
	}

	switch g {
	case Month:
		y, m, ok := strings.Cut(key, "-")
		year, month := yearNum(y), atoi(m)
		if !ok || year <= 0 || month < 1 || month > 12 || len(m) != 2 {
			return bad()
		}

		return monthColumn(time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)), nil

	case Quarter:
		y, q, ok := strings.Cut(key, "-Q")
		year, quarter := yearNum(y), atoi(q)
		if !ok || year <= 0 || quarter < 1 || quarter > 4 || len(q) != 1 {
			return bad()
		}

		from := time.Date(year, time.Month(3*(quarter-1)+1), 1, 0, 0, 0, 0, time.UTC)

		return Column{
			Key:   fmt.Sprintf("%d-Q%d", year, quarter),
			Range: Range{From: from, To: from.AddDate(0, 3, 0), Title: fmt.Sprintf("%s кв. %d", quarters[quarter-1], year)},
		}, nil

	case Year:
		year := yearNum(key)
		if year <= 0 {
			return bad()
		}

		from := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)

		return Column{
			Key:   strconv.Itoa(year),
			Range: Range{From: from, To: from.AddDate(1, 0, 0), Title: strconv.Itoa(year)},
		}, nil
	}

	return Column{}, fmt.Errorf("неизвестная гранулярность %q", g)
}

var example = map[Grain]string{Month: "2026-03", Quarter: "2026-Q1", Year: "2026"}

func monthColumn(from time.Time) Column {
	return Column{
		Key:   from.Format("2006-01"),
		Range: Range{From: from, To: from.AddDate(0, 1, 0), Title: fmt.Sprintf("%s %d", shortMonths[from.Month()-1], from.Year())},
	}
}

// yearNum — четырёхзначный год; 0 или -1, если это не он.
func yearNum(s string) int {
	if len(s) != 4 {
		return 0
	}

	return atoi(s)
}

// atoi — неотрицательное число из одних цифр или -1: strconv.Atoi пропустил
// бы знак, а «+3» в URL колонки — опечатка, а не март.
func atoi(s string) int {
	if s == "" {
		return -1
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
	}

	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}

	return n
}
