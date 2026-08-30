package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"investudy_bot/internal/parser"
	"investudy_bot/internal/sheets"
)

// Отчёт об удачном прогоне отвечает на два вопроса: доехало ли и сколько
// строк осталось без разрезов отчёта.
func TestSuccessMessage(t *testing.T) {
	msg := successMessage(parser.Report{
		SnapshotID: 42,
		Rows:       1274,
		Took:       3 * time.Second,
		Gaps:       parser.Gaps{Rows: 17, NoDivision: 12, NoItem: 9, NoPeriod: 3},
	})

	for _, want := range []string{"42", "1274", "17", "12", "9", "3"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q:\n%s", want, msg)
		}
	}
}

// Пустой список пробелов — отдельная формулировка: «0 строк без аналитики»
// читается как поломка счётчика.
func TestSuccessMessageWithoutGaps(t *testing.T) {
	msg := successMessage(parser.Report{SnapshotID: 1, Rows: 10})

	if strings.Contains(msg, "без подразделения") {
		t.Errorf("message lists gaps when there are none:\n%s", msg)
	}
	if !strings.Contains(msg, "Обязательная аналитика заполнена") {
		t.Errorf("message does not state that analytics is complete:\n%s", msg)
	}
}

// Сообщение администратору должно отвечать на один вопрос: куда идти чинить.
func TestAdminMessageForRowError(t *testing.T) {
	err := fmt.Errorf("sync: fetch: %w", &sheets.RowError{
		Sheet:        "ДДС",
		Row:          245,
		Cell:         "W245",
		Column:       "СуммаРасход",
		Value:        "12 000 тг",
		Want:         "число",
		Bank:         "Kaspi",
		Organization: "Aligee",
		Err:          errors.New("not a number"),
	})

	msg := failureMessage(err)

	for _, want := range []string{"245", "W245", "СуммаРасход", "12 000 тг", "Kaspi", "Aligee", "число"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q:\n%s", want, msg)
		}
	}
}

// Пустой банк или организация — не повод оставлять в сообщении дыру.
func TestAdminMessageFillsEmptyFields(t *testing.T) {
	msg := failureMessage(&sheets.RowError{Sheet: "ДДС", Row: 7, Cell: "L7", Column: "Период"})

	if !strings.Contains(msg, "Банк: —") || !strings.Contains(msg, "Организация: —") {
		t.Errorf("empty fields are not filled with a dash:\n%s", msg)
	}
}

// Падения, не связанные со строкой листа (база недоступна, доступ к листу
// отозван), тоже должны доезжать до администратора — с их собственным текстом.
func TestAdminMessageForPlainError(t *testing.T) {
	msg := failureMessage(errors.New("database: connect: connection refused"))

	if !strings.Contains(msg, "connection refused") {
		t.Errorf("message loses the cause:\n%s", msg)
	}
}
