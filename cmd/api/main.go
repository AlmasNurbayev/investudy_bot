// api — REST API веб-ОПиУ: вход, отчёт, админка пользователей.
//
// Демон, как бот: работает до сигнала, штатная остановка по SIGTERM ошибкой
// не считается. Проводки только читает; пишет users и sessions — то, чего
// в листе нет.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"investudy_bot/internal/api"
	"investudy_bot/internal/api/handler"
	"investudy_bot/internal/auth"
	"investudy_bot/internal/config"
	"investudy_bot/internal/db"
	"investudy_bot/internal/logger"
	"investudy_bot/internal/report"
	"investudy_bot/internal/repository"
	"investudy_bot/internal/users"
)

// shutdownGrace — сколько fiber ждёт, пока запросы допишут ответы, прежде
// чем рвать соединения. С запасом меньше 10 секунд, которые Docker ждёт
// после SIGTERM до SIGKILL.
const shutdownGrace = 5 * time.Second

func main() {
	logger.Init(slog.LevelDebug)

	cfg, err := config.LoadAPI()
	if err != nil {
		logger.ERROR("config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err = run(ctx, cfg); err != nil {
		logger.ERROR("api failed", "err", err)
		os.Exit(1)
	}
}

// run держит весь запуск в одной функции, чтобы defer'ы отработали:
// os.Exit в main их не выполняет.
func run(ctx context.Context, cfg config.APIConfig) error {
	pool, err := db.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	store := repository.NewUsers(pool)

	authSvc, err := auth.New(store)
	if err != nil {
		return err
	}

	h := handler.New(authSvc, users.New(store), report.New(repository.NewReader(pool)), store, cfg.CookieSecure)
	app := api.New(h, authSvc, cfg.Postgres.Timeout)

	if !cfg.CookieSecure {
		logger.WRN("API_COOKIE_SECURE=false: cookie сессии уйдёт и по http — только для локального запуска")
	}

	// Пул закрывается отложенным вызовом выше — уже после того, как Run
	// дождался остановки fiber: иначе Close ждал бы занятые соединения.
	return api.Run(ctx, app, cfg.Addr(), shutdownGrace)
}
