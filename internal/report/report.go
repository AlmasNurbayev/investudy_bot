// Package report собирает отчёты из данных среза.
//
// Слой между хендлерами бота и репозиторием: решает, какую версию данных брать
// и какие статьи выкинуть, но ничего не форматирует — текст для Telegram лежит
// в хендлере, как тексты для администратора лежат в cmd/parser.
package report

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"investudy_bot/internal/access"
	"investudy_bot/internal/lib/money"
	"investudy_bot/internal/lib/period"
	"investudy_bot/internal/lib/snapshot"
	"investudy_bot/internal/logger"
	"investudy_bot/internal/model"
	"investudy_bot/internal/pnl"
	"investudy_bot/internal/repository"
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
	SnapshotByID(ctx context.Context, id int64) (model.Snapshot, error)
	ClosedReportsSettings(ctx context.Context) (model.ClosedReportsSettings, error)
	ClosedReport(ctx context.Context, snapshotID int64, from, to time.Time, excluded []string) ([]model.ReportRow, error)
	PnlSettings(ctx context.Context) (model.PnlSettings, error)
	PnlStructure(ctx context.Context) (pnl.Structure, error)
	PnlFacts(ctx context.Context, snapshotID int64, cols []period.Range, divisionID int32) ([]pnl.Fact, error)
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

// PnlLimits — ограничения колонок ОПиУ из settings (ключ pnl).
//
// Нет строки или поле пустое/меньше 1 — значения из кода (period.Default*) с
// предупреждением в лог: настройка не должна ронять отчёт. Сломанный JSON —
// ошибка. Число месяцев по умолчанию не больше потолка: иначе отчёт без
// выбранных колонок нарушал бы собственное ограничение.
func (s *Service) PnlLimits(ctx context.Context) (period.Limits, error) {
	cfg, err := s.reader.PnlSettings(ctx)

	switch {
	case errors.Is(err, repository.ErrSettingNotFound):
		logger.WRN("нет настройки, беру значения по умолчанию из кода", "key", repository.PnlKey)

		cfg = model.PnlSettings{}
	case err != nil:
		return period.Limits{}, err
	}

	lim := period.Limits{
		MaxColumns:    atLeastOne(cfg.MaxColumns, period.DefaultMaxColumns, "max_columns"),
		DefaultMonths: atLeastOne(cfg.DefaultMonths, period.DefaultMonths, "default_months"),
	}

	lim.DefaultMonths = min(lim.DefaultMonths, lim.MaxColumns)

	return lim, nil
}

func atLeastOne(value, def int, name string) int {
	if value >= 1 {
		return value
	}

	logger.WRN("поле настройки пусто или меньше 1, беру по умолчанию из кода",
		"key", repository.PnlKey, "field", name, "value", value, "default", def)

	return def
}

// Snapshots — свежие версии для селектора и рабочая из них.
func (s *Service) Snapshots(ctx context.Context, limit int) ([]model.Snapshot, model.Snapshot, error) {
	snapshots, err := s.reader.ListSnapshots(ctx, limit)
	if err != nil {
		return nil, model.Snapshot{}, err
	}

	current, err := snapshot.Latest(snapshots)
	if err != nil && !errors.Is(err, snapshot.ErrNoSnapshot) {
		return nil, model.Snapshot{}, err
	}

	return snapshots, current, nil
}

// ErrSnapshotNotFound — запрошенной версии нет или она пустая.
var ErrSnapshotNotFound = errors.New("версия среза не найдена или пуста")

// PnL — ОПиУ для читателя с политикой policy.
type PnL struct {
	Snapshot model.Snapshot
	// Stale — показан не новейший срез: последняя загрузка пустая или
	// читатель выбрал старую версию сам.
	Stale   bool
	Columns []period.Column
	Report  pnl.Report
}

// PnL считает ОПиУ по колонкам. snapshotID = 0 — рабочая версия.
//
// Граница строк и подразделение берутся только из политики: обработчик
// не может «забыть» её применить, потому что других входов здесь нет.
// Неразмеченные статьи отдаются всегда — показывать ли их суммы, решает
// политика (ShowUnmapped) на выходе, а флаг неполноты нужен всем.
func (s *Service) PnL(
	ctx context.Context, policy access.Policy, snapshotID int64, cols []period.Column,
) (PnL, error) {
	newest, err := s.reader.ListSnapshots(ctx, snapshotCandidates)
	if err != nil {
		return PnL{}, err
	}

	var current model.Snapshot
	if snapshotID == 0 {
		if current, err = snapshot.Latest(newest); err != nil {
			return PnL{}, err
		}
	} else {
		if current, err = s.reader.SnapshotByID(ctx, snapshotID); err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return PnL{}, ErrSnapshotNotFound
			}

			return PnL{}, err
		}

		if !snapshot.Usable(current) {
			return PnL{}, ErrSnapshotNotFound
		}
	}

	structure, err := s.reader.PnlStructure(ctx)
	if err != nil {
		return PnL{}, err
	}

	ranges := make([]period.Range, len(cols))
	for i, c := range cols {
		ranges[i] = c.Range
	}

	facts, err := s.reader.PnlFacts(ctx, current.ID, ranges, policy.DivisionID)
	if err != nil {
		return PnL{}, err
	}

	rep, err := pnl.Build(structure, len(cols), facts, policy.LastLine)
	if err != nil {
		return PnL{}, err
	}

	return PnL{
		Snapshot: current,
		Stale:    len(newest) > 0 && newest[0].ID != current.ID,
		Columns:  cols,
		Report:   rep,
	}, nil
}
