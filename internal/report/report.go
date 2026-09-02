// Package report собирает отчёты из данных среза.
//
// Слой между хендлерами бота и репозиторием: решает, какую версию данных брать
// и какие статьи выкинуть, но ничего не форматирует — текст для Telegram лежит
// в хендлере, как тексты для администратора лежат в cmd/parser.
package report

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"investudy_bot/internal/lib/money"
	"investudy_bot/internal/lib/period"
	"investudy_bot/internal/lib/snapshot"
	"investudy_bot/internal/model"
)

// snapshotCandidates — сколько свежих версий просматривать в поисках непустой.
//
// Десяти хватает с запасом: подряд идущие пустые прогоны означают, что сломан
// парсер, и показывать данные двухнедельной давности как свежие в этом случае
// хуже, чем честно сказать, что читать нечего.
const snapshotCandidates = 10

// Reader — источник данных. Интерфейс объявлен здесь, в пакете-потребителе:
// конкретную реализацию передаёт конструктор.
type Reader interface {
	ListSnapshots(ctx context.Context, limit int) ([]model.Snapshot, error)
	ClosedReportsSettings(ctx context.Context) (model.ClosedReportsSettings, error)
	ClosedReport(ctx context.Context, snapshotID int64, from, to time.Time, excluded []string) ([]model.ReportRow, error)
}

type Service struct {
	reader Reader
}

func New(reader Reader) *Service {
	return &Service{reader: reader}
}

// Closed считает сводку по закрытому периоду.
//
// now приходит параметром, а не берётся внутри: так период считается от одного
// момента на весь вызов и тест может задать любую дату.
func (s *Service) Closed(ctx context.Context, kind period.Kind, now time.Time) (model.ClosedReport, error) {
	rng, err := period.Resolve(kind, now)
	if err != nil {
		return model.ClosedReport{}, err
	}

	snapshots, err := s.reader.ListSnapshots(ctx, snapshotCandidates)
	if err != nil {
		return model.ClosedReport{}, err
	}

	// Ошибка «непустых версий нет» уходит наружу как есть: хендлер отличает
	// её от «за период нет проводок» и говорит пользователю разные вещи.
	current, err := snapshot.Latest(snapshots)
	if err != nil {
		return model.ClosedReport{}, err
	}

	cfg, err := s.reader.ClosedReportsSettings(ctx)
	if err != nil {
		return model.ClosedReport{}, err
	}

	rows, err := s.reader.ClosedReport(ctx, current.ID, rng.From, rng.To, lower(cfg.ExcludedItems))
	if err != nil {
		return model.ClosedReport{}, err
	}

	return model.ClosedReport{
		Title:    rng.Title,
		Snapshot: current,
		// Первый элемент списка — новейшая версия; если считали не по ней,
		// значит последняя загрузка была пустой и данные не самые свежие.
		Stale:       len(snapshots) > 0 && snapshots[0].ID != current.ID,
		Rows:        rows,
		TotalDebet:  money.Sum(column(rows, func(r model.ReportRow) pgtype.Numeric { return r.Debet })),
		TotalCredit: money.Sum(column(rows, func(r model.ReportRow) pgtype.Numeric { return r.Credit })),
	}, nil
}

// lower приводит исключения к нижнему регистру: в настройке статья записана
// строчными, а в листе может стоять с заглавной, и сравнение в SQL тоже идёт
// через lower().
func lower(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = strings.ToLower(strings.TrimSpace(s))
	}

	return out
}

func column(rows []model.ReportRow, pick func(model.ReportRow) pgtype.Numeric) []pgtype.Numeric {
	out := make([]pgtype.Numeric, len(rows))
	for i, row := range rows {
		out[i] = pick(row)
	}

	return out
}
