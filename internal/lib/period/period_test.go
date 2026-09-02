package period

import (
	"testing"
	"time"
)

// at собирает момент внутри периода. Зона не UTC намеренно: Resolve обязан
// брать её из самого now, иначе границы уехали бы на пять часов.
func at(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 15, 30, 0, 0, time.FixedZone("Asia/Almaty", 5*60*60))
}

func check(t *testing.T, k Kind, now time.Time, from, to, title string) {
	t.Helper()

	r, err := Resolve(k, now)
	if err != nil {
		t.Fatalf("Resolve(%s): %v", k, err)
	}

	const layout = "2006-01-02"
	if got := r.From.Format(layout); got != from {
		t.Errorf("%s: From = %s, want %s", k, got, from)
	}
	if got := r.To.Format(layout); got != to {
		t.Errorf("%s: To = %s, want %s", k, got, to)
	}
	if r.Title != title {
		t.Errorf("%s: Title = %q, want %q", k, r.Title, title)
	}
	if r.From.Location() != now.Location() {
		t.Errorf("%s: зона потеряна: %v", k, r.From.Location())
	}
}

func TestMonths(t *testing.T) {
	now := at(2026, time.September, 2)

	check(t, CurrentMonth, now, "2026-09-01", "2026-10-01", "Сентябрь 2026")
	check(t, PreviousMonth, now, "2026-08-01", "2026-09-01", "Август 2026")
}

func TestQuarters(t *testing.T) {
	now := at(2026, time.September, 2)

	check(t, CurrentQuarter, now, "2026-07-01", "2026-10-01", "III квартал 2026")
	check(t, PreviousQuarter, now, "2026-04-01", "2026-07-01", "II квартал 2026")
}

// Переход через год — единственное место, где арифметика месяцев может соврать
// молча: январь минус месяц должен дать декабрь прошлого года, а не месяц 0.
func TestJanuaryLooksBackIntoPreviousYear(t *testing.T) {
	now := at(2026, time.January, 15)

	check(t, PreviousMonth, now, "2025-12-01", "2026-01-01", "Декабрь 2025")
	check(t, CurrentQuarter, now, "2026-01-01", "2026-04-01", "I квартал 2026")
	check(t, PreviousQuarter, now, "2025-10-01", "2026-01-01", "IV квартал 2025")
}

// Декабрь — зеркальный случай: вперёд граница обязана уехать в следующий год.
func TestDecemberLooksForwardIntoNextYear(t *testing.T) {
	now := at(2026, time.December, 31)

	check(t, CurrentMonth, now, "2026-12-01", "2027-01-01", "Декабрь 2026")
	check(t, CurrentQuarter, now, "2026-10-01", "2027-01-01", "IV квартал 2026")
}

// Тридцать первое число — классическая ловушка AddDate: 31 марта минус месяц
// даёт 3 марта, потому что февраля такой длины не бывает. Отсчёт от первого
// числа эту нормализацию исключает.
func TestEndOfLongMonthDoesNotOverflow(t *testing.T) {
	check(t, PreviousMonth, at(2026, time.March, 31), "2026-02-01", "2026-03-01", "Февраль 2026")
	check(t, PreviousMonth, at(2026, time.May, 31), "2026-04-01", "2026-05-01", "Апрель 2026")
}

// Каждый месяц квартала должен давать одни и те же границы: точка отсчёта —
// начало квартала, а не месяц пользователя.
func TestQuarterIsSameForEveryMonthInIt(t *testing.T) {
	for _, m := range []time.Month{time.July, time.August, time.September} {
		check(t, CurrentQuarter, at(2026, m, 10), "2026-07-01", "2026-10-01", "III квартал 2026")
	}
}

func TestKindsCoverAllButtons(t *testing.T) {
	for _, k := range Kinds() {
		if _, err := Resolve(k, time.Now()); err != nil {
			t.Errorf("кнопка %s не разрешается: %v", k, err)
		}
		if k.Label() == string(k) {
			t.Errorf("у кнопки %s нет подписи", k)
		}
	}
}

func TestUnknownKindIsRejected(t *testing.T) {
	if _, err := Resolve("last_century", time.Now()); err == nil {
		t.Fatal("неизвестный период принят молча")
	}
}
