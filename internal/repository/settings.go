package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"investudy_bot/internal/model"
)

// ClosedReportsKey — ключ настройки отчёта по закрытым периодам в таблице settings.
const ClosedReportsKey = "closed_reports"

// ClosedReportsSettings читает настройку отчёта.
//
// Отсутствие строки — ошибка, а не пустая настройка: без списка исключений
// сводка молча посчитается вместе с внутренними переводами и покажет задвоенные
// итоги. Пустой отчёт заметен, неверный — нет.
func (r *Reader) ClosedReportsSettings(ctx context.Context) (model.ClosedReportsSettings, error) {
	const query = `SELECT value FROM settings WHERE key = $1`

	var raw []byte
	if err := r.db.QueryRow(ctx, query, ClosedReportsKey).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.ClosedReportsSettings{}, fmt.Errorf(
				"настройка %q не найдена: накатите миграции (migrator -typeTask up)", ClosedReportsKey)
		}

		return model.ClosedReportsSettings{}, fmt.Errorf("read setting %q: %w", ClosedReportsKey, err)
	}

	var cfg model.ClosedReportsSettings
	if err := json.Unmarshal(raw, &cfg); err != nil {
		// Значение правится руками, поэтому сломанный JSON — рабочий сценарий,
		// и сказать надо, какой именно ключ чинить.
		return model.ClosedReportsSettings{}, fmt.Errorf("parse setting %q: %w", ClosedReportsKey, err)
	}

	return cfg, nil
}
