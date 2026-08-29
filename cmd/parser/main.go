package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"investudy_bot/internal/config"
	"investudy_bot/internal/db"
	"investudy_bot/internal/logger"
	"investudy_bot/internal/parser"
	"investudy_bot/internal/repository"
	"investudy_bot/internal/sheets"
)

func main() {
	logger.Init(slog.LevelDebug)

	cfg, err := config.Load()
	if err != nil {
		logger.ERROR("config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conn, err := db.New(ctx, cfg.Postgres)
	if err != nil {
		logger.ERROR("database", "err", err)
		os.Exit(1)
	}
	// Закрытие идёт по своему контексту: ctx к этому моменту уже отменён сигналом.
	defer conn.Close(context.Background())

	client, err := sheets.New(ctx, cfg.Sheets)
	if err != nil {
		logger.ERROR("sheets", "err", err)
		os.Exit(1)
	}

	logger.INF("parser started")

	// Один синк и выход: расписание — на кроне. Ненулевой код возврата
	// нужен, чтобы крон увидел неудачу.
	svc := parser.New(client, store{repository.NewStore(conn)})
	if err = svc.Sync(ctx); err != nil {
		logger.ERROR("sync", "err", err)
		os.Exit(1)
	}
}

// store подгоняет конкретный *repository.Tx под интерфейс parser.Tx: Go не считает
// метод, возвращающий конкретный тип, реализацией метода, возвращающего интерфейс.
type store struct {
	*repository.Store
}

func (s store) Begin(ctx context.Context) (parser.Tx, error) {
	return s.Store.Begin(ctx)
}
