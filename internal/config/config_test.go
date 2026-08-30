package config

import (
	"os"
	"testing"
	"time"
)

// MIN_PERIOD читается в формате листа, а не RFC3339, и остаётся необязательным:
// пустое значение означает «грузить всю историю».
func TestMinPeriod(t *testing.T) {
	fill(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("MIN_PERIOD unset: unexpected error: %v", err)
	}
	if !cfg.Sheets.MinPeriod.IsZero() {
		t.Fatalf("MIN_PERIOD unset = %v, want zero", cfg.Sheets.MinPeriod.Time)
	}

	t.Setenv("MIN_PERIOD", "01.01.2025")

	if cfg, err = Load(); err != nil {
		t.Fatalf("MIN_PERIOD set: unexpected error: %v", err)
	}

	want := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	if !cfg.Sheets.MinPeriod.Equal(want) {
		t.Fatalf("MIN_PERIOD = %v, want %v", cfg.Sheets.MinPeriod.Time, want)
	}

	// Формат обязан ругаться на входе, а не молча дать нулевую дату: нулевая
	// прошла бы как «грузить всё», то есть отсечка исчезла бы незаметно.
	t.Setenv("MIN_PERIOD", "2025-01-01")

	if _, err = Load(); err == nil {
		t.Fatal("MIN_PERIOD in ISO format: expected error")
	}
}

// Оповещать администратора не о чем, если адресат не задан, поэтому обе
// телеграм-переменные обязательные и парсер без них не стартует.
func TestTelegramRequired(t *testing.T) {
	fill(t)
	t.Setenv("TELEGRAM_ADMIN_ID", "")

	if _, err := Load(); err == nil {
		t.Fatal("empty TELEGRAM_ADMIN_ID: expected error")
	}
}

func fill(t *testing.T) {
	t.Helper()

	for k, v := range map[string]string{
		"SPREADSHEET_ID":          "sheet-id",
		"SHEET_NAME":              "ДДС",
		"GOOGLE_CREDENTIALS_FILE": "/creds.json",
		"POSTGRES_HOST":           "localhost",
		"POSTGRES_PORT":           "5432",
		"POSTGRES_DB":             "investudy",
		"POSTGRES_USER":           "investudy",
		"POSTGRES_PASSWORD":       "secret",
		"DB_TIMEOUT":              "30s",
		"TELEGRAM_BOT_TOKEN":      "token",
		"TELEGRAM_ADMIN_ID":       "42",
		"MIN_PERIOD":              "",
	} {
		t.Setenv(k, v)
	}

	// Пустое значение и незаданная переменная для env — разные вещи, а нужна
	// именно незаданная. Setenv выше уже повесил откат к исходному состоянию,
	// поэтому Unsetenv не течёт в соседние тесты.
	if err := os.Unsetenv("MIN_PERIOD"); err != nil {
		t.Fatal(err)
	}
}
