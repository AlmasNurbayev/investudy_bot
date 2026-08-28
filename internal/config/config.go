package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	Sheets   SheetsConfig
	Postgres PostgresConfig
}

type SheetsConfig struct {
	SpreadsheetID string
	SheetName     string
}

type PostgresConfig struct {
	Host     string
	Port     string
	Database string
	User     string
	Password string
	Timeout  time.Duration
}

func Load() (Config, error) {
	var missing []string

	get := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}

	dbTimeout, err := time.ParseDuration(get("DB_TIMEOUT"))
	if err != nil && len(missing) == 0 {
		return Config{}, fmt.Errorf("DB_TIMEOUT: %w", err)
	}

	cfg := Config{
		Sheets: SheetsConfig{
			SpreadsheetID: get("SPREADSHEET_ID"),
			SheetName:     get("SHEET_NAME"),
		},
		Postgres: PostgresConfig{
			Host:     get("POSTGRES_HOST"),
			Port:     get("POSTGRES_PORT"),
			Database: get("POSTGRES_DB"),
			User:     get("POSTGRES_USER"),
			Password: get("POSTGRES_PASSWORD"),
			Timeout:  dbTimeout,
		},
	}

	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %v", missing)
	}

	return cfg, nil
}
