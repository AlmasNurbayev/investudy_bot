package logger

import (
	"log/slog"
	"os"
)

// Значение по умолчанию, а не nil: вызов до Init иначе роняет процесс nil-паникой.
// Init только перенастраивает вывод.
var l = slog.Default()

func Init(level slog.Level) {
	l = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(l)
}

func DBG(msg string, args ...any) {
	l.Debug(msg, args...)
}

func INF(msg string, args ...any) {
	l.Info(msg, args...)
}

func WRN(msg string, args ...any) {
	l.Warn(msg, args...)
}

func ERROR(msg string, args ...any) {
	l.Error(msg, args...)
}
