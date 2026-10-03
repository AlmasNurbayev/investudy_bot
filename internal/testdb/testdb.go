// Package testdb — общая база интеграционных тестов.
//
// Тесты нескольких пакетов (repository, api) работают с одной базой из
// TEST_DATABASE_URL и начинают с TRUNCATE. go test ./... гоняет пакеты
// параллельно, и без сериализации один пакет стирал бы пользователей и
// справочники посреди теста другого — так тесты и падали в CI.
//
// Сериализация — advisory-блокировкой Postgres на весь прогон пакета, а не
// флагом -p 1: флаг надо помнить в каждом месте запуска (make, CI, IDE),
// а блокировка действует при любом.
package testdb

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// lockKey — ключ advisory-блокировки; любое число, лишь бы одно на все пакеты.
const lockKey = 7_190_003

// EnvDSN — переменная с адресом тестовой базы.
const EnvDSN = "TEST_DATABASE_URL"

// Run выполняет тесты пакета, держа блокировку общей базы: вызывать из
// TestMain. Без TEST_DATABASE_URL просто выполняет тесты — интеграционные
// пропустятся сами.
func Run(m *testing.M) int {
	dsn := os.Getenv(EnvDSN)
	if dsn == "" {
		return m.Run()
	}

	ctx := context.Background()

	// Отдельное соединение на весь прогон: advisory-блокировка уровня
	// сессии живёт, пока живо соединение, которое её взяло.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "testdb: connect: %v\n", err)
		return 1
	}
	defer func() { _ = conn.Close(ctx) }()

	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		fmt.Fprintf(os.Stderr, "testdb: lock: %v\n", err)
		return 1
	}

	// Снимать явно не обязательно — закрытие соединения снимет её само,
	// — но так следующий пакет не ждёт, пока соединение закроется.
	defer func() { _, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, lockKey) }()

	return m.Run()
}
