// bot — Telegram-бот: отдаёт пользователям отчёты по данным из PostgreSQL.
//
// В отличие от остальных бинарников — демон: работает до сигнала, а не до
// конца одного прогона. Данные только читает, писать в базу имеет право
// исключительно парсер.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"investudy_bot/internal/bot"
	"investudy_bot/internal/config"
	"investudy_bot/internal/db"
	"investudy_bot/internal/logger"
	"investudy_bot/internal/report"
	"investudy_bot/internal/repository"
)

func main() {
	logger.Init(slog.LevelDebug)

	cfg, err := config.LoadBot()
	if err != nil {
		logger.ERROR("config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err = run(ctx, cfg); err != nil {
		logger.ERROR("bot failed", "err", err)
		os.Exit(1)
	}
}

// run держит весь запуск в одной функции, чтобы defer'ы отработали:
// os.Exit в main их не выполняет.
func run(ctx context.Context, cfg config.BotConfig) error {
	pool, err := db.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	reader := repository.NewReader(pool)

	b, err := bot.New(cfg.Telegram, report.New(reader), reader)
	if err != nil {
		return err
	}

	return b.Run(ctx)
}
