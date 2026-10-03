package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"investudy_bot/internal/model"
)

// ErrSettingNotFound — в settings нет строки с таким ключом. Отдельной ошибкой,
// чтобы вызывающий сам решал: отчёт без настройки молча считать нельзя, а срок
// сессии вполне можно взять по умолчанию.
var ErrSettingNotFound = errors.New("настройка не найдена")

// ClosedReportsKey — ключ настройки отчёта по закрытым периодам в таблице settings.
const ClosedReportsKey = "closed_reports"

// AuthKey и PnlKey — ключи настроек сайта в таблице settings.
const (
	AuthKey = "auth"
	PnlKey  = "pnl"
)

// ClosedReportsSettings читает настройку отчёта.
//
// Отсутствие строки — ошибка, а не пустая настройка: без списка исключений
// сводка молча посчитается вместе с внутренними переводами и покажет задвоенные
// итоги. Пустой отчёт заметен, неверный — нет.
func (r *Reader) ClosedReportsSettings(ctx context.Context) (model.ClosedReportsSettings, error) {
	var cfg model.ClosedReportsSettings

	err := readSetting(ctx, r.db, ClosedReportsKey, &cfg)

	return cfg, err
}

// AuthSettings читает настройки входа на сайт. Нет строки — ErrSettingNotFound:
// что делать без неё, решает auth.
func (u *Users) AuthSettings(ctx context.Context) (model.AuthSettings, error) {
	var cfg model.AuthSettings

	err := readSetting(ctx, u.db, AuthKey, &cfg)

	return cfg, err
}

// PnlSettings читает настройки колонок ОПиУ. Нет строки — ErrSettingNotFound:
// значения по умолчанию подставляет вызывающий.
func (r *Reader) PnlSettings(ctx context.Context) (model.PnlSettings, error) {
	var cfg model.PnlSettings

	err := readSetting(ctx, r.db, PnlKey, &cfg)

	return cfg, err
}

// readSetting читает строку settings по ключу в dst.
//
// Значение правится руками, поэтому сломанный JSON — рабочий сценарий, и
// сказать надо, какой именно ключ чинить.
func readSetting(ctx context.Context, q Querier, key string, dst any) error {
	const query = `SELECT value FROM settings WHERE key = $1`

	var raw []byte
	if err := q.QueryRow(ctx, query, key).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %q, накатите миграции (migrator -typeTask up)", ErrSettingNotFound, key)
		}

		return fmt.Errorf("read setting %q: %w", key, err)
	}

	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("parse setting %q: %w", key, err)
	}

	return nil
}
