package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"investudy_bot/internal/config"
)

// maxConns — потолок соединений пула.
//
// Четырёх достаточно: бот делает редкие тяжёлые агрегаты, а не поток мелких
// запросов, и держать под каждого пользователя своё соединение незачем.
const maxConns = 4

// Pool — пул соединений для долгоживущих сервисов.
//
// Conn боту не годится: за ним один *pgx.Conn, а его нельзя использовать из
// нескольких горутин, тогда как апдейты Telegram обрабатываются параллельно.
// Одноразовые бинарники остаются на Conn — им пул не нужен.
type Pool struct {
	pool *pgxpool.Pool
	cfg  config.PostgresConfig
}

func NewPool(ctx context.Context, cfg config.PostgresConfig) (*Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	poolCfg.MaxConns = maxConns

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	// Пул подключается лениво, поэтому без пинга бот стартовал бы «успешно»
	// с недоступной базой и сообщил бы об этом первому же пользователю.
	pingCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	if err = pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	return &Pool{pool: pool, cfg: cfg}, nil
}

func (p *Pool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return p.pool.Query(ctx, sql, args...)
}

func (p *Pool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return p.pool.QueryRow(ctx, sql, args...)
}

// Begin открывает транзакцию: тем самым Pool подходит и на место repository.Beginner.
func (p *Pool) Begin(ctx context.Context) (pgx.Tx, error) {
	return p.pool.Begin(ctx)
}

// Ctx возвращает контекст с настроенным таймаутом запроса.
func (p *Pool) Ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, p.cfg.Timeout)
}

func (p *Pool) Close() {
	p.pool.Close()
}
