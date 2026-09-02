package handler

import (
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/guregu/null/v6"
	"github.com/jackc/pgx/v5/pgtype"

	"investudy_bot/internal/model"
)

func num(t *testing.T, s string) pgtype.Numeric {
	t.Helper()

	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		t.Fatalf("scan %q: %v", s, err)
	}

	return n
}

func sampleReport(t *testing.T) model.ClosedReport {
	t.Helper()

	rows := []model.ReportRow{
		{Division: "Алматы", Item: "Аренда", SubItem: "Офис", Debet: num(t, "1200000.00"), Credit: num(t, "0")},
		{Division: "Алматы", Item: "Аренда", SubItem: "Склад", Debet: num(t, "300000.00"), Credit: num(t, "0")},
		{Division: "Алматы", Item: "Выручка", SubItem: "—", Debet: num(t, "0"), Credit: num(t, "980200.00")},
		{Division: "Астана", Item: "Аренда", SubItem: "Офис", Debet: num(t, "500000.00"), Credit: num(t, "0")},
	}

	return model.ClosedReport{
		Title:       "Сентябрь 2026",
		Snapshot:    model.Snapshot{ID: 142, TakenAt: null.TimeFrom(time.Date(2026, 9, 2, 3, 10, 0, 0, time.UTC))},
		Rows:        rows,
		TotalDebet:  num(t, "2000000.00"),
		TotalCredit: num(t, "980200.00"),
	}
}

func TestRenderClosed(t *testing.T) {
	out := renderClosed(sampleReport(t))
	if len(out) != 1 {
		t.Fatalf("сообщений %d, want 1", len(out))
	}

	text := out[0]

	for _, want := range []string{"Сентябрь 2026", "Срез №142 от 02.09.2026 03:10", "Алматы", "Астана", "ИТОГО"} {
		if !strings.Contains(text, want) {
			t.Errorf("в отчёте нет %q", want)
		}
	}

	// Статья печатается один раз на группу подстатей: повтор превратил бы
	// иерархию в плоский список.
	if n := strings.Count(text, "· Аренда"); n != 2 {
		t.Errorf("«Аренда» как заголовок статьи встречается %d раз, want 2 (по одному на подразделение)", n)
	}
}

// Разметка смешанная: заголовки — форматированный текст, чтобы переноситься
// по ширине экрана, таблица — <pre>, чтобы держать колонки и прокручиваться
// вбок вместо переноса.
func TestRenderClosedMarkup(t *testing.T) {
	text := renderClosed(sampleReport(t))[0]

	for _, want := range []string{
		"<b>Закрытые периоды · Сентябрь 2026</b>",
		"<i>Срез №142 от 02.09.2026 03:10</i>",
		"<b>Алматы</b>",
		"<b>ИТОГО</b>",
		"<pre>",
		"Дебет",
		"Кредит",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в отчёте нет %q", want)
		}
	}

	// Названия подразделений — вне <pre>: внутри они не переносились бы
	// и уезжали за край вместе с таблицей.
	if strings.Contains(text, "<pre>🏢") {
		t.Error("заголовок подразделения попал внутрь моноширинного блока")
	}
}

// Шапка про подменённую версию — единственное, что отличает вчерашние данные
// от сегодняшних; без неё откат на предыдущий срез незаметен.
func TestRenderClosedMarksStale(t *testing.T) {
	rep := sampleReport(t)

	if strings.Contains(renderClosed(rep)[0], "предыдущий срез") {
		t.Error("свежий отчёт помечен как устаревший")
	}

	rep.Stale = true
	if !strings.Contains(renderClosed(rep)[0], "предыдущий срез") {
		t.Error("устаревший отчёт не помечен")
	}
}

// Пустой период и отсутствие данных вообще — разные новости, и пустое
// сообщение вместо ответа выглядело бы как сбой.
func TestRenderClosedWithoutRows(t *testing.T) {
	rep := sampleReport(t)
	rep.Rows = nil

	out := renderClosed(rep)
	if len(out) != 1 || !strings.Contains(out[0], "данных нет") {
		t.Errorf("пустой отчёт = %q", out)
	}
}

// Названия приезжают из Google Sheets: неэкранированный «<» Telegram примет
// за тег и отвергнет всё сообщение целиком.
func TestRenderClosedEscapesNames(t *testing.T) {
	rep := sampleReport(t)
	rep.Rows[0].SubItem = "R&D <под ключ>"

	text := renderClosed(rep)[0]

	if strings.Contains(text, "<под") {
		t.Error("угловые скобки из названия не заэкранированы")
	}
	if !strings.Contains(text, "R&amp;D") {
		t.Error("амперсанд из названия не заэкранирован")
	}
	// Собственная разметка отчёта при этом обязана уцелеть.
	if !strings.Contains(text, "<pre>") || !strings.Contains(text, "<b>") {
		t.Error("разметка отчёта потеряна")
	}
}

// Длинный отчёт режется на сообщения; резать его молча по лимиту нельзя —
// пропавшие строки в таблице ничем себя не выдают.
func TestRenderClosedSplitsLongReport(t *testing.T) {
	rep := sampleReport(t)
	rep.Rows = nil
	for i := range 400 {
		rep.Rows = append(rep.Rows, model.ReportRow{
			Division: "Подразделение " + string(rune('А'+i%32)),
			Item:     "Статья",
			SubItem:  "Подстатья",
			Debet:    num(t, "1000.00"),
			Credit:   num(t, "0"),
		})
	}

	out := renderClosed(rep)
	if len(out) < 2 {
		t.Fatalf("сообщений %d, ожидалось разбиение", len(out))
	}

	for i, msg := range out {
		if len(msg) > 4096 {
			t.Errorf("сообщение %d длиной %d — сверх лимита Telegram", i, len(msg))
		}
		if strings.Count(msg, "<pre>") != strings.Count(msg, "</pre>") {
			t.Errorf("сообщение %d с незакрытым <pre>", i)
		}
	}

	// Итог обязан доехать: без него отчёт бесполезен.
	if !strings.Contains(out[len(out)-1], "ИТОГО") {
		t.Error("итоговая строка потерялась при разбиении")
	}
}

// Разрезанная секция продолжается под своим заголовком: таблица, начавшаяся
// в новом сообщении без подписи, не читается вовсе.
func TestSplitSectionKeepsItsTitle(t *testing.T) {
	rep := sampleReport(t)
	rep.Rows = nil
	for i := range 300 {
		rep.Rows = append(rep.Rows, model.ReportRow{
			Division: "Алматы",
			Item:     "Аренда",
			SubItem:  "Подстатья " + string(rune('А'+i%32)),
			Debet:    num(t, "1000.00"),
			Credit:   num(t, "0"),
		})
	}

	out := renderClosed(rep)
	if len(out) < 2 {
		t.Fatalf("сообщений %d, ожидалось разбиение", len(out))
	}

	if !strings.Contains(out[1], "Алматы") {
		t.Error("продолжение секции осталось без заголовка")
	}
	if !strings.Contains(out[1], "продолжение") {
		t.Error("продолжение не помечено — выглядит как второе подразделение с тем же именем")
	}
}

// Колонки держатся выравниванием по рунам: fmt считает байты, и на кириллице
// таблица разъехалась бы вдвое.
func TestColumnsAlignOnCyrillic(t *testing.T) {
	short := row("Офис", num(t, "1.00"), num(t, "2.00"))
	long := row("Подразделение обслуживания", num(t, "1.00"), num(t, "2.00"))

	if a, b := len([]rune(short)), len([]rune(long)); a != b {
		t.Errorf("ширина строк разъехалась: %d и %d рун", a, b)
	}
}

// Слишком длинное название обрезается, но с многоточием: две статьи с общим
// началом иначе выглядят в отчёте одинаково.
func TestLongNameIsClipped(t *testing.T) {
	line := row(strings.Repeat("я", 80), num(t, "1.00"), num(t, "2.00"))

	if !strings.Contains(line, "…") {
		t.Error("обрезка не помечена многоточием")
	}
	if n := len([]rune(line)); n != nameWidth+2*amountWidth {
		t.Errorf("длина строки %d рун, want %d", n, nameWidth+2*amountWidth)
	}
}

// Шапка не должна уезжать в сообщение одна: раскладка обязана вычитать её
// место из лимита заранее, а не выталкивать первую секцию целиком.
func TestFirstMessageCarriesData(t *testing.T) {
	rep := sampleReport(t)
	rep.Rows = nil
	for i := range 300 {
		rep.Rows = append(rep.Rows, model.ReportRow{
			Division: "Алматы",
			Item:     "Аренда",
			SubItem:  "Подстатья " + string(rune('А'+i%32)),
			Debet:    num(t, "1000.00"),
			Credit:   num(t, "0"),
		})
	}

	first := renderClosed(rep)[0]
	if !strings.Contains(first, "<pre>") {
		t.Error("первое сообщение ушло с одной шапкой, без таблицы")
	}
}

// Reply-клавиатура предыдущей версии бота живёт на клиенте и переживает смену
// бота: снять её можно только явной разметкой, а прицепить её удаётся лишь
// к сообщениям без инлайн-кнопок — на сообщение приходится один reply_markup.
func TestPlainMessagesRemoveStaleKeyboard(t *testing.T) {
	got := orRemoveKeyboard(nil)

	remove, ok := got.(*models.ReplyKeyboardRemove)
	if !ok {
		t.Fatalf("разметка = %T, want *models.ReplyKeyboardRemove", got)
	}
	if !remove.RemoveKeyboard {
		t.Error("remove_keyboard=false — чужие кнопки останутся на экране")
	}
}

// А своя инлайн-клавиатура снятием не подменяется: иначе выбор периода исчез бы.
func TestInlineKeyboardSurvives(t *testing.T) {
	markup := &models.InlineKeyboardMarkup{}

	if got := orRemoveKeyboard(markup); got != models.ReplyMarkup(markup) {
		t.Errorf("инлайн-клавиатура подменена на %T", got)
	}
}
