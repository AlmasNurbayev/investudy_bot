package api_test

import (
	"os"
	"testing"

	"investudy_bot/internal/testdb"
)

// TestMain держит общую тестовую базу на весь прогон пакета: другие пакеты
// с интеграционными тестами ждут, а не чистят её посреди наших тестов.
func TestMain(m *testing.M) {
	os.Exit(testdb.Run(m))
}
