// Package period считает границы отчётных периодов — месяца и квартала.
//
// Вынесен отдельно от отчёта, потому что границу «прошлого месяца» одинаково
// нужны и боту, и будущему веб-отчёту, и ошибиться в ней легко: арифметика дат
// в Go полна ловушек, а проверить её глазами по результату отчёта невозможно.
package period

import (
	"fmt"
	"time"
)

// Kind — отчётный период, выбираемый кнопкой.
//
// Значения короткие и латиницей: они уезжают в callback_data кнопки, где
// Telegram даёт 64 байта, а кириллица тратит по два на символ.
type Kind string

const (
	CurrentMonth    Kind = "cur_month"
	PreviousMonth   Kind = "prev_month"
	CurrentQuarter  Kind = "cur_quarter"
	PreviousQuarter Kind = "prev_quarter"
)

// Kinds — периоды в порядке кнопок клавиатуры.
func Kinds() []Kind {
	return []Kind{CurrentMonth, PreviousMonth, CurrentQuarter, PreviousQuarter}
}

var labels = map[Kind]string{
	CurrentMonth:    "Текущий месяц",
	PreviousMonth:   "Прошлый месяц",
	CurrentQuarter:  "Текущий квартал",
	PreviousQuarter: "Прошлый квартал",
}

// Label — подпись кнопки.
func (k Kind) Label() string {
	if s, ok := labels[k]; ok {
		return s
	}

	return string(k)
}

// Range — полуинтервал [From, To): To в период не входит.
//
// Полуинтервал, а не BETWEEN с последним днём: он не требует знать, сколько
// дней в месяце, и не оставляет щели между «до 23:59:59» и следующей полуночью.
type Range struct {
	From  time.Time
	To    time.Time
	Title string
}

var months = [...]string{
	"Январь", "Февраль", "Март", "Апрель", "Май", "Июнь",
	"Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь",
}

var quarters = [...]string{"I", "II", "III", "IV"}

// Resolve считает границы периода относительно now.
//
// Зона берётся из самого now и в пакете не зашита. Она уже задана дважды —
// в postgresql.conf и в generated-колонках snapshots, — и третье место, которое
// придётся держать в согласии с ними, ничего не улучшит: процесс бота получает
// её из TZ контейнера там же, где её получает Postgres.
func Resolve(k Kind, now time.Time) (Range, error) {
	loc := now.Location()
	y, m := now.Year(), now.Month()

	// Первое число месяца — единственная безопасная точка отсчёта. AddDate по
	// произвольному дню нормализует переполнение вперёд: 31 марта минус месяц
	// даёт 3 марта, а не февраль. time.Date с месяцем 0 или 13 нормализуется
	// в соседний год сам, поэтому переход через год отдельного кода не требует.
	monthStart := func(offset int) time.Time {
		return time.Date(y, m+time.Month(offset), 1, 0, 0, 0, 0, loc)
	}

	// Начало квартала, в который попадает now: месяцы нумеруются с 1, поэтому
	// смещение внутри квартала — (m-1) % 3.
	quarterStart := func(offset int) time.Time {
		return time.Date(y, m-time.Month((int(m)-1)%3)+time.Month(offset), 1, 0, 0, 0, 0, loc)
	}

	switch k {
	case CurrentMonth:
		from := monthStart(0)
		return Range{From: from, To: monthStart(1), Title: monthTitle(from)}, nil

	case PreviousMonth:
		from := monthStart(-1)
		return Range{From: from, To: monthStart(0), Title: monthTitle(from)}, nil

	case CurrentQuarter:
		from := quarterStart(0)
		return Range{From: from, To: quarterStart(3), Title: quarterTitle(from)}, nil

	case PreviousQuarter:
		from := quarterStart(-3)
		return Range{From: from, To: quarterStart(0), Title: quarterTitle(from)}, nil
	}

	return Range{}, fmt.Errorf("unknown period %q", k)
}

func monthTitle(from time.Time) string {
	return fmt.Sprintf("%s %d", months[from.Month()-1], from.Year())
}

func quarterTitle(from time.Time) string {
	return fmt.Sprintf("%s квартал %d", quarters[(int(from.Month())-1)/3], from.Year())
}
