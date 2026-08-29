// Мигратор накатывает схему БД и завершается.
//
// Запускается до парсера и бота — код возврата и есть протокол: ненулевой
// не даст сервису стартовать на несовпадающей схеме. В образе это выражено
// цепочкой в CMD (см. Dockerfile):
//
//	/app/migrator -typeTask up && exec /app/parser
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"

	"github.com/golang-migrate/migrate/v4"
	// Драйвер регистрирует себя под схемой pgx5 (см. resolveDSN).
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"investudy_bot/internal/config"
	"investudy_bot/internal/logger"
	migrations "investudy_bot/migrate"
)

const (
	taskUp   = "up"
	taskDown = "down"
)

func main() {
	logger.Init(slog.LevelDebug)

	task := flag.String("typeTask", taskUp,
		"up — накатить все миграции; down — откатить последнюю")
	dsn := flag.String("dsn", "",
		"адрес БД; если не задан, собирается из POSTGRES_* переменных")
	flag.Parse()

	if err := run(*task, *dsn); err != nil {
		logger.ERROR("migrate", "err", err)
		os.Exit(1)
	}
}

func run(task, dsn string) error {
	addr, err := resolveDSN(dsn)
	if err != nil {
		return err
	}

	// Миграции вшиты в бинарник, а не читаются с диска: так они не могут
	// разъехаться с кодом, и в рантайм-образ не нужно копировать каталог.
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return err
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, addr)
	if err != nil {
		return err
	}
	defer func() {
		if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
			logger.WRN("close migrator", "source", srcErr, "database", dbErr)
		}
	}()

	before, dirty, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return err
	}

	// Грязное состояние остаётся после миграции, упавшей на середине. Чинить его
	// автоматически нельзя: неизвестно, что успело примениться, а force на неверную
	// версию тихо разошёлся бы со схемой.
	if dirty {
		return fmt.Errorf(
			"database is dirty at version %d: разобрать вручную и снять флаг через migrate force", before)
	}

	switch task {
	case taskUp:
		err = m.Up()
	case taskDown:
		// Ровно один шаг назад: m.Down() снёс бы всю схему разом.
		err = m.Steps(-1)
	default:
		return fmt.Errorf("unknown -typeTask %q: ожидается %s или %s", task, taskUp, taskDown)
	}

	if err != nil {
		if errors.Is(err, migrate.ErrNoChange) {
			logger.INF("schema is up to date", "version", before)
			return nil
		}
		return err
	}

	after, _, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return err
	}

	logger.INF("migrations applied", "task", task, "from", before, "to", after)

	return nil
}

// resolveDSN приводит адрес к схеме pgx5 — под этим именем golang-migrate
// регистрирует драйвер, обычный postgres:// он не распознает. Это позволяет
// передавать в -dsn стандартную строку подключения без переделки.
func resolveDSN(dsn string) (string, error) {
	if dsn == "" {
		cfg, err := config.LoadPostgres()
		if err != nil {
			return "", err
		}

		return cfg.MigrateDSN(), nil
	}

	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse -dsn: %w", err)
	}

	if u.Scheme == "postgres" || u.Scheme == "postgresql" {
		u.Scheme = "pgx5"
	}

	return u.String(), nil
}
