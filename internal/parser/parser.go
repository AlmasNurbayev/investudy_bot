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
//
// Чистки истории здесь нет: ею занимается отдельный бинарник prunedb со своим
// расписанием. Парсер только добавляет версии.
type Store interface {
	Begin(ctx context.Context) (Tx, error)
}

// Tx — транзакция одного синка.
type Tx interface {
	BeginSnapshot(ctx context.Context) (int64, error)
	InsertRows(ctx context.Context, snapshotID int64, rows []model.Row) (int64, error)
	FinishSnapshot(ctx context.Context, snapshotID, rowCount int64) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Report — итог прогона. Возвращается наружу, а не рассылается изнутри:
// оповещением заведует cmd/parser, чтобы все тексты для администратора —
// и про успех, и про падение — лежали в одном месте.
type Report struct {
	SnapshotID int64
	Rows       int64
	Took       time.Duration
	Gaps       Gaps
}

// Gaps — строки без обязательной аналитики.
//
// Без подразделения, статьи или периода проводка не попадает ни в один разрез
// отчёта: она есть в базе, но невидима в любой группировке. Это не ошибка
// загрузки, поэтому синк из-за неё не падает, но и молчать нельзя — иначе
// пропажа обнаружится только расхождением итогов.
type Gaps struct {
	// Rows — строк, где не хватает хотя бы одного из трёх. Меньше суммы
	// остальных полей: в одной строке обычно пусто сразу несколько.
	Rows       int
	NoDivision int
	NoItem     int
	NoPeriod   int
}

type Service struct {
	sheets Fetcher
	store  Store
}

func New(sheets Fetcher, store Store) *Service {
	return &Service{sheets: sheets, store: store}
}

// Sync читает лист и публикует новую версию среза одной транзакцией.
//
// Одноразовый запуск: расписанием заведует крон, а не тикер внутри процесса.
// Поэтому ошибка возвращается наружу — код возврата и есть сигнал крону.
func (s *Service) Sync(ctx context.Context) (Report, error) {
	started := time.Now()

	rows, err := s.sheets.Fetch(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("fetch: %w", err)
	}

	// Пустой лист почти всегда означает отозванный доступ или сбитый диапазон.
	// Опубликовать такой срез — значит показать пользователям пустой отчёт
	// и оставить эту пустоту в истории, поэтому лучше упасть.
	if len(rows) == 0 {
		return Report{}, errors.New("fetch: sheet returned no rows")
	}

	snapshotID, rowCount, err := s.publish(ctx, rows)
	if err != nil {
		return Report{}, err
	}

	report := Report{
		SnapshotID: snapshotID,
		Rows:       rowCount,
		Took:       time.Since(started),
		Gaps:       countGaps(rows),
	}

	logger.INF("snapshot published",
		"snapshot_id", report.SnapshotID, "rows", report.Rows, "took", report.Took,
		"rows_without_analytics", report.Gaps.Rows)

	return report, nil
}

// countGaps считает строки без обязательной аналитики.
//
// Обязательный минимум — подразделение, статья и период: по ним строятся все
// разрезы отчёта. Справочники здесь ещё именами, а не id, поэтому пустота
// проверяется по пустой строке — репозиторий превратит её в NULL во внешнем
// ключе.
func countGaps(rows []model.Row) Gaps {
	var g Gaps

	for _, row := range rows {
		missing := false

		if row.Division == "" {
			g.NoDivision++
			missing = true
		}
		if row.Item == "" {
			g.NoItem++
			missing = true
		}
		if !row.Period.Valid {
			g.NoPeriod++
			missing = true
		}

		if missing {
			g.Rows++
		}
	}

	return g
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
