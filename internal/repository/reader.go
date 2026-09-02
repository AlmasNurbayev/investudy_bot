package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"investudy_bot/internal/model"
)

// Querier — источник запросов (его реализует internal/db.Pool).
//
// Отдельно от Beginner: читающему коду транзакции не нужны, а требовать их
// значило бы разрешить ему писать.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Reader — чтение данных для отчётов. Ничего не пишет: писать в базу имеет
// право только парсер.
type Reader struct {
	db Querier
}

func NewReader(db Querier) *Reader {
	return &Reader{db: db}
}

// ListSnapshots отдаёт свежие версии, новейшие первыми. Какая из них рабочая,
// решает internal/lib/snapshot: пустой срез читать незачем.
func (r *Reader) ListSnapshots(ctx context.Context, limit int) ([]model.Snapshot, error) {
	const query = `
		SELECT id, taken_at, row_count, year, month, week
		FROM snapshots
		ORDER BY taken_at DESC
		LIMIT $1`

	rows, err := r.db.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	defer rows.Close()

	var snapshots []model.Snapshot
	for rows.Next() {
		var s model.Snapshot
		if err = rows.Scan(&s.ID, &s.TakenAt, &s.RowCount, &s.Year, &s.Month, &s.Week); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}

		snapshots = append(snapshots, s)
	}
	// Ошибка запроса приходит здесь, а не из Query: pgx отдаёт её при обходе.
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}

	return snapshots, nil
}

// closedReport — сводка по разрезам подразделение → статья → подстатья.
//
// Читается data с явным snapshot_id, а не вью data_current: она прибита
// к новейшему срезу по taken_at безусловно, а отчёт показывает новейший
// непустой. Забыть версию нельзя — она обязательный параметр метода.
//
// snapshot_id стоит в WHERE первым: под него и построен data_snapshot_period_idx
// (snapshot_id, period), так что индекс отрабатывает целиком.
//
// Джойны левые: все FK в data nullable, и строка без аналитики обязана попасть
// в сводку прочерком, а не исчезнуть — парсер такие строки специально считает
// и докладывает администратору, прятать их в отчёте было бы непоследовательно.
//
// Исключения сравниваются по lower(): в настройках статья записана строчными,
// в листе может оказаться с заглавной. Проверка на NULL обязательна —
// NULL <> ALL (...) даёт NULL, и строки без статьи выпали бы из отчёта молча.
const closedReport = `
	SELECT coalesce(dv.name, '—') AS division,
	       coalesce(it.name, '—') AS item,
	       coalesce(si.name, '—') AS sub_item,
	       coalesce(sum(d.debet), 0)  AS debet,
	       coalesce(sum(d.credit), 0) AS credit
	FROM data d
	LEFT JOIN divisions dv ON dv.id = d.division_id
	LEFT JOIN items     it ON it.id = d.item_id
	LEFT JOIN sub_items si ON si.id = d.sub_item_id
	WHERE d.snapshot_id = $1
	  AND d.period >= $2
	  AND d.period <  $3
	  AND (it.name IS NULL OR lower(it.name) <> ALL ($4::text[]))
	GROUP BY 1, 2, 3
	ORDER BY 1, 2, 3`

// ClosedReport считает сводку по периоду [from, to) внутри одной версии среза.
//
// Границы — полуинтервал: верхняя не входит, поэтому знать длину месяца
// не требуется и щели между периодами не остаётся.
func (r *Reader) ClosedReport(
	ctx context.Context, snapshotID int64, from, to time.Time, excluded []string,
) ([]model.ReportRow, error) {
	// nil в text[] уходит как NULL, и тогда условие исключений даёт NULL
	// на каждой строке — отчёт оказался бы пустым. Пустой срез даёт '{}'.
	if excluded == nil {
		excluded = []string{}
	}

	rows, err := r.db.Query(ctx, closedReport, snapshotID, from, to, excluded)
	if err != nil {
		return nil, fmt.Errorf("closed report for snapshot %d: %w", snapshotID, err)
	}
	defer rows.Close()

	var report []model.ReportRow
	for rows.Next() {
		var row model.ReportRow
		if err = rows.Scan(&row.Division, &row.Item, &row.SubItem, &row.Debet, &row.Credit); err != nil {
			return nil, fmt.Errorf("scan report row: %w", err)
		}

		report = append(report, row)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("closed report for snapshot %d: %w", snapshotID, err)
	}

	return report, nil
}
