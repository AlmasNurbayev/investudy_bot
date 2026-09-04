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

// Ключ сервис-аккаунта задаётся ровно одним способом. Ни одного — парсеру
// нечем ходить в Sheets; оба — в конфиге два разных ключа, и молчаливый выбор
// победителя увёл бы загрузку в чужую таблицу.
func TestCredentialsSource(t *testing.T) {
	cases := []struct {
		name       string
		credential string
		file       string
		err        bool
	}{
		{name: "file only", file: "/creds.json"},
		{name: "value only", credential: `{"type":"service_account"}`},
		{name: "neither", err: true},
		{name: "both", credential: `{"type":"service_account"}`, file: "/creds.json", err: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fill(t)
			t.Setenv("GOOGLE_CREDENTIALS_FILE", c.file)
			t.Setenv("GOOGLE_CREDENTIALS", c.credential)

			cfg, err := Load()

			if c.err {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.Sheets.CredentialsFile != c.file || cfg.Sheets.Credentials != c.credential {
				t.Fatalf("credentials = %q / file = %q, want %q / %q",
					cfg.Sheets.Credentials, cfg.Sheets.CredentialsFile, c.credential, c.file)
			}
		})
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
