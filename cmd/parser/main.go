package main

import (
	"fmt"
	"log"
	"os"
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
}

func configFromEnv() (Config, error) {
	var missing []string

	get := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
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
		},
	}

	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %v", missing)
	}

	return cfg, nil
}

func main() {
	cfg, err := configFromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	_ = cfg
}
