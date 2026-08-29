package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"investudy_bot/internal/config"
)

type Repo struct {
	conn *pgx.Conn
	cfg  config.PostgresConfig
}

func New(ctx context.Context, cfg config.PostgresConfig) (*Repo, error) {
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

	return &Repo{conn: conn, cfg: cfg}, nil
}

// Begin opens a transaction. Каждый синк парсера идёт своей транзакцией: заливка
// становится видимой читателям одним COMMIT, а падение не оставляет следов.
func (r *Repo) Begin(ctx context.Context) (pgx.Tx, error) {
	return r.conn.Begin(ctx)
}

// Ctx returns a context with the configured query timeout.
func (r *Repo) Ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), r.cfg.Timeout)
}

// Close закрывает соединение. Ошибка закрытия неинформативна — процесс всё равно
// завершается, поэтому глотаем её явно.
func (r *Repo) Close(ctx context.Context) {
	_ = r.conn.Close(ctx)
}
