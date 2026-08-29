package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"investudy_bot/internal/config"
)

// Conn — подключение к PostgreSQL и источник транзакций. Запросов не выполняет:
// работа с данными живёт в internal/repository.
type Conn struct {
	conn *pgx.Conn
	cfg  config.PostgresConfig
}

func New(ctx context.Context, cfg config.PostgresConfig) (*Conn, error) {
	connCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	conn, err := pgx.Connect(connCtx, cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	if err = conn.Ping(connCtx); err != nil {
		_ = conn.Close(ctx)
		return nil, fmt.Errorf("ping: %w", err)
	}

	return &Conn{conn: conn, cfg: cfg}, nil
}

// Begin открывает транзакцию. Каждый синк парсера идёт своей: заливка становится
// видимой читателям одним COMMIT, а падение не оставляет следов.
func (c *Conn) Begin(ctx context.Context) (pgx.Tx, error) {
	return c.conn.Begin(ctx)
}

// Ctx возвращает контекст с настроенным таймаутом запроса.
func (c *Conn) Ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), c.cfg.Timeout)
}

// Close закрывает соединение. Ошибка закрытия неинформативна — процесс всё равно
// завершается, поэтому глотаем её явно.
func (c *Conn) Close(ctx context.Context) {
	_ = c.conn.Close(ctx)
}
