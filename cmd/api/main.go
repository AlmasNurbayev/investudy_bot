// api — REST API веб-ОПиУ: вход, отчёт, админка пользователей.
//
// Демон, как бот: работает до сигнала, штатная остановка по SIGTERM ошибкой
// не считается. Проводки только читает; пишет users и sessions — то, чего
// в листе нет.
//
// Первый администратор сайта заводится той же командой:
//
//	api -create-admin -login almas [-role cfo] [-username "Алмас"]
//
// Пароль спрашивается без эха с терминала или читается первой строкой
// stdin — в аргументах он попал бы в историю шелла и в список процессов.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"investudy_bot/internal/access"
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

// shutdownGrace — сколько начатые запросы получают на то, чтобы закончиться
// самим при остановке; потом они прерываются. С запасом меньше 10 секунд,
// которые Docker ждёт после SIGTERM до SIGKILL.
const shutdownGrace = 5 * time.Second

func main() {
	logger.Init(slog.LevelDebug)

	createAdmin := flag.Bool("create-admin", false, "завести администратора сайта и выйти")
	login := flag.String("login", "", "логин администратора (с -create-admin)")
	role := flag.String("role", string(access.CFO), "роль администратора (с -create-admin)")
	username := flag.String("username", "", "имя для списка пользователей (с -create-admin)")
	flag.Parse()

	cfg, err := config.LoadAPI()
	if err != nil {
		logger.ERROR("config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *createAdmin {
		err = runCreateAdmin(ctx, cfg, *login, *username, access.Role(*role))
	} else {
		err = run(ctx, cfg)
	}

	if err != nil {
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
	srv := api.New(h, authSvc, cfg.Postgres.Timeout)

	if !cfg.CookieSecure {
		logger.WRN("API_COOKIE_SECURE=false: cookie сессии уйдёт и по http — только для локального запуска")
	}

	// Пул закрывается отложенным вызовом выше — уже после того, как Run
	// дождался или прервал все запросы: иначе Close ждал бы занятые соединения.
	return srv.Run(ctx, cfg.Addr, shutdownGrace)
}

func runCreateAdmin(ctx context.Context, cfg config.APIConfig, login, username string, role access.Role) error {
	if strings.TrimSpace(login) == "" {
		return errors.New("-create-admin: укажите -login")
	}

	password, err := readPassword()
	if err != nil {
		return err
	}

	pool, err := db.NewPool(ctx, cfg.Postgres)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	u, err := users.New(repository.NewUsers(pool)).CreateAdmin(ctx, login, username, role, password)
	if err != nil {
		var invalid users.ValidationError
		if errors.As(err, &invalid) {
			return fmt.Errorf("администратор не заведён: %s", strings.Join(invalid.Problems, "; "))
		}

		return err
	}

	logger.INF("admin created", "id", u.ID, "login", u.Login.String, "role", u.Role)

	return nil
}

// readPassword спрашивает пароль дважды без эха, если stdin — терминал,
// иначе читает первую строку (для запуска из скрипта: `api ... < file`).
func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())

	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}

		return strings.TrimRight(line, "\r\n"), nil
	}

	ask := func(prompt string) (string, error) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)

		return string(b), err
	}

	first, err := ask("Пароль: ")
	if err != nil {
		return "", err
	}

	second, err := ask("Ещё раз: ")
	if err != nil {
		return "", err
	}

	if first != second {
		return "", errors.New("пароли не совпали")
	}

	return first, nil
}
