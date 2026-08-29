// prunedb чистит историю снепшотов по заданной схеме и завершается.
//
// Отдельный бинарник со своим расписанием: парсер только добавляет версии,
// а сколько их хранить — вопрос политики, а не загрузки. Запускается кроном,
// код возврата — сигнал о неудаче.
//
//	prunedb -scheme monthly
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"strings"

	"investudy_bot/internal/config"
	"investudy_bot/internal/db"
	"investudy_bot/internal/logger"
	"investudy_bot/internal/repository"
)

func main() {
	logger.Init(slog.LevelDebug)

	scheme := flag.String("scheme", repository.SchemeMonthly,
		"схема чистки; доступны: "+strings.Join(repository.Schemes(), ", "))
	flag.Parse()

	if err := run(*scheme); err != nil {
		logger.ERROR("prune", "err", err)
		os.Exit(1)
	}
}

func run(scheme string) error {
	cfg, err := config.LoadPostgres()
	if err != nil {
		return err
	}

	ctx := context.Background()

	conn, err := db.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())

	deleted, err := repository.NewStore(conn).Prune(ctx, scheme)
	if err != nil {
		return err
	}

	// Строки data уносит ON DELETE CASCADE, поэтому считаем версии, а не строки.
	logger.INF("snapshots pruned", "scheme", scheme, "deleted", deleted)

	return nil
}
