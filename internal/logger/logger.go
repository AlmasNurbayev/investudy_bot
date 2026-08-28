package logger

import (
	"log/slog"
	"os"
)

var l *slog.Logger

func Init(level slog.Level) {
	l = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(l)
}

func DBG(msg string, args ...any) {
	l.Debug(msg, args...)
}

func WRN(msg string, args ...any) {
	l.Warn(msg, args...)
}

func ERROR(msg string, args ...any) {
	l.Error(msg, args...)
}
