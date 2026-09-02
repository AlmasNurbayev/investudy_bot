package repository

import (
	"context"
	"fmt"
)

// UserAllowed проверяет, есть ли пользователь в белом списке.
//
// Администратор из TELEGRAM_ADMIN_ID сюда не попадает и проверяется отдельно
// в боте: сразу после миграции таблица пуста, и без такого обхода выдать доступ
// первому пользователю было бы некому.
func (r *Reader) UserAllowed(ctx context.Context, telegramID int64) (bool, error) {
	const query = `SELECT EXISTS (SELECT 1 FROM users WHERE telegram_id = $1)`

	var allowed bool
	if err := r.db.QueryRow(ctx, query, telegramID).Scan(&allowed); err != nil {
		return false, fmt.Errorf("check user %d: %w", telegramID, err)
	}

	return allowed, nil
}
