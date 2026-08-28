package main

import (
	"context"
	"log/slog"
	"os"

	"investudy_bot/internal/config"
	"investudy_bot/internal/logger"
	"investudy_bot/internal/db"
)

func main() {
	logger.Init(slog.LevelDebug)

	cfg, err := config.Load()
	if err != nil {
		logger.ERROR("config", "err", err)
		os.Exit(1)
	}

	ctx := context.Background()

	repo, err := db.New(ctx, cfg.Postgres)
	if err != nil {
		logger.ERROR("repository", "err", err)
		os.Exit(1)
	}
	defer repo.Close(ctx)
	defer repo.Commit(ctx)
}
