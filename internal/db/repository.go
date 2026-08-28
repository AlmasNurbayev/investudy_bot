package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"investudy_bot/internal/config"
)

type Repo struct {
	conn    *pgx.Conn
	Tx      pgx.Tx
	cfg     config.PostgresConfig
}

func New(ctx context.Context, cfg config.PostgresConfig) (*Repo, error) {
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database)

	connCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	conn, err := pgx.Connect(connCtx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	if err = conn.Ping(connCtx); err != nil {
		conn.Close(ctx)
		return nil, fmt.Errorf("ping: %w", err)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Close(ctx)
		return nil, fmt.Errorf("begin tx: %w", err)
	}

	return &Repo{conn: conn, Tx: tx, cfg: cfg}, nil
}

// Ctx returns a context with the configured query timeout.
func (r *Repo) Ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), r.cfg.Timeout)
}

func (r *Repo) Commit(ctx context.Context) error {
	return r.Tx.Commit(ctx)
}

func (r *Repo) Rollback(ctx context.Context) error {
	return r.Tx.Rollback(ctx)
}

func (r *Repo) Close(ctx context.Context) {
	r.conn.Close(ctx)
}
