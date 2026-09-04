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
	SpreadsheetID string `env:"SPREADSHEET_ID,required,notEmpty"`
	SheetName     string `env:"SHEET_NAME,required,notEmpty"`

	// Ключ сервис-аккаунта задаётся ровно одним из двух способов.
	//
	// Credentials — сам ключ значением переменной: так контейнеру не нужен
	// bind-mount ради одного файла, а секрет едет тем же путём, что пароль базы
	// и токен бота. Принимается и голый JSON, и его base64: JSON лезет в .env
	// одной строкой (переносы внутри private_key экранированы самим форматом),
	// но кавычки и обратные слэши переживают не каждый разборщик .env, а base64
	// не содержит ничего, что мог бы съесть make, compose или шелл.
	//
	// CredentialsFile — путь к файлу, как раньше: на машине разработчика ключ
	// уже лежит файлом, и перекладывать его в .env ради одного прогона незачем.
	Credentials     string `env:"GOOGLE_CREDENTIALS"`
	CredentialsFile string `env:"GOOGLE_CREDENTIALS_FILE"`

	// MinPeriod — нижняя граница загрузки: строки с period раньше этой даты
	// не грузятся вовсе. Необязательная: пустое значение означает «грузить всё».
	MinPeriod SheetDate `env:"MIN_PERIOD"`
}

// validate проверяет, что источник ключа ровно один.
//
// Оба разом — не «одно перекрывает другое», а ошибка: два ключа в конфиге
// означают, что кто-то правил не тот, и молчаливый выбор победителя увёл бы
// парсер в чужую таблицу. Ни одного — сразу, а не при первом обращении к Sheets.
func (c SheetsConfig) validate() error {
	switch {
	case c.Credentials == "" && c.CredentialsFile == "":
		return fmt.Errorf("set GOOGLE_CREDENTIALS (service account key itself, JSON or its base64) or GOOGLE_CREDENTIALS_FILE (path to it)")
	case c.Credentials != "" && c.CredentialsFile != "":
		return fmt.Errorf("GOOGLE_CREDENTIALS and GOOGLE_CREDENTIALS_FILE are both set, keep one")
	}

	return nil
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

// BotConfig — настройки бота: база и доступы в Telegram.
//
// Списка разрешённых пользователей здесь нет намеренно: он лежит в таблице
// users, потому что доступ выдают и отзывают чаще, чем перезапускают сервис.
type BotConfig struct {
	Postgres PostgresConfig
	Telegram TelegramConfig
}

// LoadBot читает настройки бота. Load потребовал бы ещё и доступы к Google
// Sheets, которых у бота нет и быть не должно: пишет в базу только парсер.
func LoadBot() (BotConfig, error) {
	var cfg BotConfig

	if err := env.Parse(&cfg); err != nil {
		return BotConfig{}, err
	}

	return cfg, nil
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

	// Взаимоисключающие переменные тегами env не выражаются: required повесить
	// нельзя ни на одну из двух, поэтому проверка идёт отдельным шагом.
	if err := cfg.Sheets.validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}
