package auth

import (
	"context"
	"errors"
	"time"

	"investudy_bot/internal/logger"
	"investudy_bot/internal/model"
	"investudy_bot/internal/repository"
)

// Значения по умолчанию из кода: действуют, когда строки auth в settings нет
// или нужное поле в ней пустое либо меньше 1. Обычные значения приезжают
// миграцией 000004; эти — аварийные, чтобы настройка не роняла вход.
//
// Срок сессии по умолчанию — самый короткий, сутки: ошибка настройки не должна
// превращаться в долгую сессию. Остальные совпадают с сидом.
const (
	defaultSessionDays    = 1
	defaultMaxFailures    = 5
	defaultWindowMinutes  = 15
	defaultMinPasswordLen = 10
)

// Policy — действующие настройки входа.
type Policy struct {
	// SessionTTL — скользящий срок сессии.
	SessionTTL time.Duration
	// MaxFailures неудач подряд с пары «логин + адрес» запирают вход...
	MaxFailures int
	// ...на FailureWindow, который же является окном подсчёта.
	FailureWindow time.Duration
	// MinPasswordLen — минимальная длина пароля в символах.
	MinPasswordLen int
}

// SettingsReader — источник настроек входа (реализует repository.Users).
type SettingsReader interface {
	AuthSettings(ctx context.Context) (model.AuthSettings, error)
}

// LoadPolicy читает настройки входа и подставляет значения по умолчанию.
//
// Нет строки — всё по умолчанию. Пустое поле или меньше 1 — по умолчанию для
// этого поля. Оба случая пишутся в лог предупреждением: это аварийный режим, а
// не обычная работа. Сломанный JSON — ошибка: настройку поправили руками
// неверно, и это надо чинить, а не обходить.
//
// Читается при входе, при продлении сессии и при заведении пароля, а не при
// старте: настройки меняют без перезапуска.
func LoadPolicy(ctx context.Context, r SettingsReader) (Policy, error) {
	cfg, err := r.AuthSettings(ctx)

	switch {
	case errors.Is(err, repository.ErrSettingNotFound):
		logger.WRN("нет настройки, беру значения по умолчанию из кода", "key", repository.AuthKey)

		cfg = model.AuthSettings{}
	case err != nil:
		return Policy{}, err
	}

	return Policy{
		SessionTTL:     time.Duration(orDefault(cfg.SessionTTLDays, defaultSessionDays, "session_ttl_days")) * 24 * time.Hour,
		MaxFailures:    orDefault(cfg.LoginMaxFailures, defaultMaxFailures, "login_max_failures"),
		FailureWindow:  time.Duration(orDefault(cfg.LoginWindowMinutes, defaultWindowMinutes, "login_window_minutes")) * time.Minute,
		MinPasswordLen: orDefault(cfg.MinPasswordLength, defaultMinPasswordLen, "min_password_length"),
	}, nil
}

// orDefault возвращает value, а при значении меньше 1 — def.
func orDefault(value, def int, name string) int {
	if value >= 1 {
		return value
	}

	logger.WRN("поле настройки пусто или меньше 1, беру по умолчанию из кода",
		"key", repository.AuthKey, "field", name, "value", value, "default", def)

	return def
}
