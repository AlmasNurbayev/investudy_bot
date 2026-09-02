package handler

import (
	"fmt"
	"html"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"investudy_bot/internal/lib/money"
	"investudy_bot/internal/model"
)

// maxMessage — потолок одного сообщения.
//
// Лимит Telegram — 4096 символов; запас оставлен под разметку, упереться
// в лимит означает получить ошибку вместо отчёта.
const maxMessage = 3600

// Ширины колонок таблицы. Без фиксированных сумма уезжает вслед за длиной
// названия, и колонку чисел становится невозможно читать сверху вниз.
const (
	nameWidth   = 22
	amountWidth = 16
)

// renderClosed печатает сводку и режет её на сообщения.
//
// Разметка смешанная, и это осознанно. Заголовки, названия подразделений
// и итоги — обычный форматированный текст: он переносится по ширине экрана
// и читается на телефоне. Сами строки таблицы — <pre>: только моноширинный
// блок держит колонки и, в отличие от <code>, прокручивается вбок вместо
// переноса, а перенесённая строка таблицы теряет всякий смысл.
func renderClosed(rep model.ClosedReport) []string {
	head := header(rep)

	if len(rep.Rows) == 0 {
		return []string{head + "\n\n<i>За период данных нет.</i>"}
	}

	sections := make([]section, 0, len(rep.Rows))
	for _, d := range groupByDivision(rep.Rows) {
		sections = append(sections, renderDivision(d))
	}

	sections = append(sections, section{
		title: "💰 <b>ИТОГО</b>",
		lines: []string{
			row("", debetTitle, creditTitle),
			row("Все подразделения", rep.TotalDebet, rep.TotalCredit),
		},
	})

	return pack(head, sections)
}

func header(rep model.ClosedReport) string {
	var b strings.Builder

	fmt.Fprintf(&b, "📊 <b>Закрытые периоды · %s</b>", esc(rep.Title))

	taken := "—"
	if rep.Snapshot.TakenAt.Valid {
		taken = rep.Snapshot.TakenAt.Time.Format("02.01.2006 15:04")
	}
	fmt.Fprintf(&b, "\n<i>Срез №%d от %s</i>", rep.Snapshot.ID, taken)

	// Про подмену версии молчать нельзя: вчерашние данные, выданные без
	// оговорки, выглядят как сегодняшние, и расхождение всплывёт не здесь.
	if rep.Stale {
		b.WriteString("\n⚠️ <i>последняя загрузка пустая, показан предыдущий срез</i>")
	}

	return b.String()
}

// section — озаглавленный кусок отчёта: подразделение или итог.
//
// Заголовок хранится отдельно от строк, чтобы при разбиении на сообщения его
// можно было повторить: таблица без подписи, начавшаяся в новом сообщении,
// не читается вовсе.
type section struct {
	title string
	lines []string
}

// division — строки одного подразделения.
type division struct {
	name string
	rows []model.ReportRow
}

// groupByDivision режет выборку на подразделения одним проходом: порядок уже
// задан ORDER BY в запросе, пересортировывать нечего.
func groupByDivision(rows []model.ReportRow) []division {
	var out []division

	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1].name != r.Division {
			out = append(out, division{name: r.Division})
		}

		last := &out[len(out)-1]
		last.rows = append(last.rows, r)
	}

	return out
}

// renderDivision печатает подразделение: статьи с подстатьями и итог.
func renderDivision(d division) section {
	lines := []string{row("", debetTitle, creditTitle)}

	var lastItem string
	for i, r := range d.rows {
		if i == 0 || r.Item != lastItem {
			lines = append(lines, "· "+r.Item)
			lastItem = r.Item
		}

		lines = append(lines, row("   "+r.SubItem, r.Debet, r.Credit))
	}

	// Итог подразделения складывается здесь, а не в SQL: ROLLUP добавил бы
	// в выборку строки, которые пришлось бы отличать от обычных по NULL.
	lines = append(lines, row("Итого",
		money.Sum(column(d.rows, func(r model.ReportRow) pgtype.Numeric { return r.Debet })),
		money.Sum(column(d.rows, func(r model.ReportRow) pgtype.Numeric { return r.Credit }))))

	return section{title: "🏢 <b>" + esc(d.name) + "</b>", lines: lines}
}

func column(rows []model.ReportRow, pick func(model.ReportRow) pgtype.Numeric) []pgtype.Numeric {
	out := make([]pgtype.Numeric, len(rows))
	for i, r := range rows {
		out[i] = pick(r)
	}

	return out
}

// Заголовки колонок печатаются той же функцией, что и строки, — так они
// не могут разъехаться с данными.
const (
	debetTitle  = "Дебет"
	creditTitle = "Кредит"
)

// row собирает строку таблицы. Суммы принимаются и числом, и готовой
// строкой — заголовок колонки выравнивается по тем же правилам.
func row(name string, debet, credit any) string {
	return padRight(clip(name, nameWidth), nameWidth) +
		padLeft(amount(debet), amountWidth) +
		padLeft(amount(credit), amountWidth)
}

func amount(v any) string {
	switch a := v.(type) {
	case string:
		return a
	case pgtype.Numeric:
		return money.Format(a)
	}

	return ""
}

// Выравнивание считается по рунам, а не по байтам: %-*s у fmt меряет байты,
// и на кириллице колонка уехала бы вдвое.
func padRight(s string, width int) string {
	if n := width - utf8.RuneCountInString(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}

	return s
}

func padLeft(s string, width int) string {
	if n := width - utf8.RuneCountInString(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}

	return s
}

// clip укорачивает название до ширины колонки. Обрезка помечается многоточием:
// иначе две разные статьи с общим началом выглядят в отчёте как одна.
func clip(s string, width int) string {
	r := []rune(s)
	if len(r) < width {
		return s
	}

	return string(r[:width-2]) + "…"
}

// pack раскладывает секции по сообщениям.
//
// Секция может не влезть целиком — тогда она продолжается в следующем
// сообщении под тем же заголовком с пометкой. Обрезать отчёт по лимиту нельзя:
// недостающие строки в таблице ничем себя не выдают, и итог просто не сойдётся.
func pack(head string, sections []section) []string {
	// Место под шапку и заголовок вычитается из лимита заранее, на все куски
	// разом. Иначе первый кусок мог бы не влезть в сообщение вместе с шапкой,
	// а отправлять шапку отдельным сообщением — значит слать пустое.
	limit := maxMessage - len(head) - longestTitle(sections) - len("<pre></pre>\n\n\n")

	var parts []string
	for _, s := range sections {
		title := s.title

		for _, chunk := range chunks(s.lines, limit) {
			parts = append(parts, title+"\n"+table(chunk))
			// Второй и дальнейшие куски помечаются: иначе продолжение
			// выглядит как ещё одно подразделение с тем же именем.
			title = s.title + " <i>(продолжение)</i>"
		}
	}

	var (
		out []string
		cur strings.Builder
	)
	cur.WriteString(head)

	for _, part := range parts {
		block := "\n\n" + part

		if cur.Len()+len(block) > maxMessage {
			out = append(out, cur.String())
			cur.Reset()
			block = part
		}

		cur.WriteString(block)
	}

	return append(out, cur.String())
}

func longestTitle(sections []section) int {
	longest := 0
	for _, s := range sections {
		if n := len(s.title); n > longest {
			longest = n
		}
	}

	// Запас на пометку «(продолжение)», которой в исходных заголовках нет.
	return longest + len(" <i>(продолжение)</i>")
}

// table оборачивает строки в моноширинный блок. Экранируется весь блок разом:
// названия приезжают из Google Sheets, и «<» в них Telegram принял бы за тег
// и отверг сообщение целиком.
func table(lines []string) string {
	return "<pre>" + esc(strings.Join(lines, "\n")) + "</pre>"
}

// chunks режет строки секции на куски, влезающие в лимит.
func chunks(lines []string, limit int) [][]string {
	var (
		out  [][]string
		cur  []string
		size int
	)

	for _, line := range lines {
		// Одна строка длиннее лимита невозможна: её ширина фиксирована
		// колонками, поэтому проверять здесь нечего.
		if size+len(line)+1 > limit && len(cur) > 0 {
			out = append(out, cur)
			cur, size = nil, 0
		}

		cur = append(cur, line)
		size += len(line) + 1
	}

	return append(out, cur)
}

func esc(s string) string {
	return html.EscapeString(s)
}
