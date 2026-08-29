package parser

import (
	"context"
	"errors"
	"fmt"
	"time"

	"investudy_bot/internal/logger"
	"investudy_bot/internal/model"
)

// Fetcher — источник данных (Google Sheets).
type Fetcher interface {
	Fetch(ctx context.Context) ([]model.Row, error)
}

// Store — запись срезов в БД.
type Store interface {
	Begin(ctx context.Context) (Tx, error)
	Prune(ctx context.Context, retentionWeeks int) (int64, error)
}

// Tx — транзакция одного синка.
type Tx interface {
	BeginSnapshot(ctx context.Context) (int64, error)
	InsertRows(ctx context.Context, snapshotID int64, rows []model.Row) (int64, error)
	FinishSnapshot(ctx context.Context, snapshotID, rowCount int64) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

type Service struct {
	sheets         Fetcher
	store          Store
	retentionWeeks int
}

func New(sheets Fetcher, store Store, retentionWeeks int) *Service {
	return &Service{sheets: sheets, store: store, retentionWeeks: retentionWeeks}
}

// Sync читает лист и публикует новую версию среза одной транзакцией.
//
// Одноразовый запуск: расписанием заведует крон, а не тикер внутри процесса.
// Поэтому ошибка возвращается наружу — код возврата и есть сигнал крону.
func (s *Service) Sync(ctx context.Context) error {
	started := time.Now()

	rows, err := s.sheets.Fetch(ctx)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	// Пустой лист почти всегда означает отозванный доступ или сбитый диапазон.
	// Опубликовать такой срез — значит показать пользователям пустой отчёт и
	// оставить эту пустоту в истории на всю неделю, поэтому лучше упасть.
	if len(rows) == 0 {
		return errors.New("fetch: sheet returned no rows")
	}

	snapshotID, rowCount, err := s.publish(ctx, rows)
	if err != nil {
		return err
	}

	logger.INF("snapshot published",
		"snapshot_id", snapshotID, "rows", rowCount, "took", time.Since(started))

	// Прунинг идёт после публикации и своей транзакцией: опубликованный срез от
	// него не зависит, поэтому его ошибка — повод предупредить, а не завалить синк.
	deleted, err := s.store.Prune(ctx, s.retentionWeeks)
	if err != nil {
		logger.WRN("prune", "err", err)
		return nil
	}

	if deleted > 0 {
		logger.INF("snapshots pruned", "deleted", deleted)
	}

	return nil
}

func (s *Service) publish(ctx context.Context, rows []model.Row) (int64, int64, error) {
	tx, err := s.store.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	// Откат при любом раннем выходе. После успешного Commit он вернёт ErrTxClosed —
	// это ожидаемо, поэтому ошибку глотаем явно.
	defer func() { _ = tx.Rollback(ctx) }()

	snapshotID, err := tx.BeginSnapshot(ctx)
	if err != nil {
		return 0, 0, err
	}

	rowCount, err := tx.InsertRows(ctx, snapshotID, rows)
	if err != nil {
		return 0, 0, err
	}

	if err = tx.FinishSnapshot(ctx, snapshotID, rowCount); err != nil {
		return 0, 0, err
	}

	if err = tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit snapshot %d: %w", snapshotID, err)
	}

	return snapshotID, rowCount, nil
}
