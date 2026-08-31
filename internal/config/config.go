package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	Sheets   SheetsConfig
	Postgres PostgresConfig
	Telegram TelegramConfig
}

type SheetsConfig struct {
	SpreadsheetID   string `env:"SPREADSHEET_ID,required,notEmpty"`
	SheetName       string `env:"SHEET_NAME,required,notEmpty"`
	CredentialsFile string `env:"GOOGLE_CREDENTIALS_FILE,required,notEmpty"`
	// MinPeriod — нижняя граница загрузки: строки с period раньше этой даты
	// не грузятся вовсе. Необязательная: пустое значение означает «грузить всё».
	MinPeriod SheetDate `env:"MIN_PERIOD"`
}

// TelegramConfig — доступы бота. Парсеру они нужны не для чтения апдейтов,
// а чтобы сообщить администратору о неудачной загрузке.
//
// Обе переменные обязательные, и намеренно: молча загружать данные, не имея
// канала для жалобы, хуже, чем не стартовать вовсе — о падениях тогда никто
// не узнает до первого расхождения в отчёте.
type TelegramConfig struct {
	Token   string `env:"TELEGRAM_BOT_TOKEN,required,notEmpty"`
	AdminID int64  `env:"TELEGRAM_ADMIN_ID,required,notEmpty"`
}

// SheetDate — дата в формате самого листа (`02.01.2006`).
//
// В .env её пишет человек, который смотрит в колонку «Период», поэтому формат
// тот же, что он там видит. Голый time.Time не годится: env разбирает его
// через encoding.TextUnmarshaler, то есть требовал бы RFC3339.
type SheetDate struct {
	time.Time
}

const sheetDateLayout = "02.01.2006"

func (d *SheetDate) UnmarshalText(text []byte) error {
	s := strings.TrimSpace(string(text))
	if s == "" {
		*d = SheetDate{}
		return nil
	}

	t, err := time.Parse(sheetDateLayout, s)
	if err != nil {
		return fmt.Errorf("expected date as %s, got %q", sheetDateLayout, s)
	}

	*d = SheetDate{t}

	return nil
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

// sslmode=disable задан явно, хотя pgx и без него подключится: по умолчанию
// действует prefer — попытка TLS, отказ сервера (ssl выключен в
// pg_conf/postgresql.conf) и тихий откат на открытый текст. Лишний round-trip
// не страшен, а вот диагностика страдает: отказ TLS остаётся в ошибке первой
// строкой, и настоящая причина сбоя («нет такой базы», «нет записи в
// pg_hba.conf») прячется под ней. База слушает loopback, TLS на ней не поднят —
// шифровать нечего, и притворяться, что могли бы, незачем.
func (c PostgresConfig) dsn(scheme string) string {
	u := url.URL{
		Scheme:   scheme,
		User:     url.UserPassword(c.User, c.Password),
		Host:     net.JoinHostPort(c.Host, c.Port),
		Path:     c.Database,
		RawQuery: "sslmode=disable",
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
