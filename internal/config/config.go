package config

import (
	"net"
	"net/url"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	Sheets   SheetsConfig
	Postgres PostgresConfig
}

type SheetsConfig struct {
	SpreadsheetID   string `env:"SPREADSHEET_ID,required,notEmpty"`
	SheetName       string `env:"SHEET_NAME,required,notEmpty"`
	CredentialsFile string `env:"GOOGLE_CREDENTIALS_FILE,required,notEmpty"`
}

type PostgresConfig struct {
	Host     string        `env:"POSTGRES_HOST,required,notEmpty"`
	Port     string        `env:"POSTGRES_PORT,required,notEmpty"`
	Database string        `env:"POSTGRES_DB,required,notEmpty"`
	User     string        `env:"POSTGRES_USER,required,notEmpty"`
	Password string        `env:"POSTGRES_PASSWORD,required,notEmpty"`
	Timeout  time.Duration `env:"DB_TIMEOUT,required,notEmpty"`
}

// DSN — адрес подключения для pgx.
//
// Собирается через url.URL, а не Sprintf: пароль со спецсимволами (@, /, :)
// иначе разъехался бы по частям адреса.
func (c PostgresConfig) DSN() string {
	return c.dsn("postgres")
}

// MigrateDSN — тот же адрес со схемой pgx5: под этим именем golang-migrate
// регистрирует драйвер database/pgx/v5.
func (c PostgresConfig) MigrateDSN() string {
	return c.dsn("pgx5")
}

func (c PostgresConfig) dsn(scheme string) string {
	u := url.URL{
		Scheme: scheme,
		User:   url.UserPassword(c.User, c.Password),
		Host:   net.JoinHostPort(c.Host, c.Port),
		Path:   c.Database,
	}

	return u.String()
}

// LoadPostgres читает только настройки БД. Мигратору не нужны ни доступы
// к Google Sheets, ни токен бота, а Load потребовал бы их все.
func LoadPostgres() (PostgresConfig, error) {
	var cfg PostgresConfig

	if err := env.Parse(&cfg); err != nil {
		return PostgresConfig{}, err
	}

	return cfg, nil
}

func Load() (Config, error) {
	var cfg Config

	// env.Parse собирает все проблемы разом, а не падает на первой,
	// поэтому недостающие переменные видно одним списком.
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}
