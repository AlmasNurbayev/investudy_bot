package repository

import (
	"context"
	"fmt"
	"time"

	"investudy_bot/internal/lib/period"
	"investudy_bot/internal/pnl"
)

// PnlStructure читает строки ОПиУ и разметку статей.
func (r *Reader) PnlStructure(ctx context.Context) (pnl.Structure, error) {
	const linesQuery = `SELECT id, code, title, kind, sort, expand FROM pnl_lines ORDER BY sort`

	rows, err := r.db.Query(ctx, linesQuery)
	if err != nil {
		return pnl.Structure{}, fmt.Errorf("read pnl lines: %w", err)
	}
	defer rows.Close()

	var s pnl.Structure
	for rows.Next() {
		var (
			l      pnl.Line
			kind   string
			expand []string
		)
		if err = rows.Scan(&l.ID, &l.Code, &l.Title, &kind, &l.Sort, &expand); err != nil {
			return pnl.Structure{}, fmt.Errorf("scan pnl line: %w", err)
		}

		l.Kind = pnl.Kind(kind)
		for _, d := range expand {
			l.Expand = append(l.Expand, pnl.Dim(d))
		}

		s.Lines = append(s.Lines, l)
	}
	if err = rows.Err(); err != nil {
		return pnl.Structure{}, fmt.Errorf("read pnl lines: %w", err)
	}

	// Пустая структура — не «отчёт из одних неразмеченных», а ненакатанная
	// миграция: сказать надо прямо.
	if len(s.Lines) == 0 {
		return pnl.Structure{}, fmt.Errorf("структура ОПиУ пуста: накатите миграции (migrator -typeTask up)")
	}

	const mapQuery = `SELECT item_name, line_id FROM pnl_item_map`

	rows, err = r.db.Query(ctx, mapQuery)
	if err != nil {
		return pnl.Structure{}, fmt.Errorf("read pnl item map: %w", err)
	}
	defer rows.Close()

	s.Items = map[string]*int32{}
	for rows.Next() {
		var (
			name string
			line *int32
		)
		if err = rows.Scan(&name, &line); err != nil {
			return pnl.Structure{}, fmt.Errorf("scan pnl item map: %w", err)
		}

		s.Items[name] = line
	}
	if err = rows.Err(); err != nil {
		return pnl.Structure{}, fmt.Errorf("read pnl item map: %w", err)
	}

	return s, nil
}

// PnlFacts читает агрегаты ОПиУ по колонкам внутри одной версии среза.
//
// Колонки любой гранулярности — просто список полуинтервалов, поэтому запрос
// один на отчёт, а не по запросу на колонку. divisionID = 0 — все
// подразделения; иначе фильтр руководителя отдела стоит в WHERE, до
// агрегации: чужие суммы не попадают ни в одну строку и ни в один итог.
//
// Отсечение по period, а не по date: ОПиУ строится по учётным периодам.
func (r *Reader) PnlFacts(
	ctx context.Context, snapshotID int64, cols []period.Range, divisionID int32,
) ([]pnl.Fact, error) {
	// Джойны левые по той же причине, что в ClosedReport: строка без аналитики
	// обязана дойти до движка — без статьи она станет неразмеченной, без
	// подразделения — пустым узлом раскрытия, но не исчезнет.
	//
	// snapshot_id и period в WHERE — под data_snapshot_period_idx.
	const query = `
		SELECT c.idx - 1,
		       coalesce(it.name, ''),
		       coalesce(d.division_id, 0), coalesce(dv.name, ''),
		       coalesce(d.sub_item_id, 0), coalesce(si.name, ''),
		       sum(d.sum_dash)
		FROM data d
		JOIN unnest($2::date[], $3::date[]) WITH ORDINALITY AS c (from_, to_, idx)
		  ON d.period >= c.from_ AND d.period < c.to_
		LEFT JOIN items     it ON it.id = d.item_id
		LEFT JOIN divisions dv ON dv.id = d.division_id
		LEFT JOIN sub_items si ON si.id = d.sub_item_id
		WHERE d.snapshot_id = $1
		  AND d.period >= $5 AND d.period < $6
		  AND ($4::int = 0 OR d.division_id = $4)
		  AND d.sum_dash IS NOT NULL
		GROUP BY 1, 2, 3, 4, 5, 6`

	if len(cols) == 0 {
		return nil, nil
	}

	froms, tos := make([]time.Time, len(cols)), make([]time.Time, len(cols))
	lo, hi := cols[0].From, cols[0].To
	for i, c := range cols {
		froms[i], tos[i] = c.From, c.To
		if c.From.Before(lo) {
			lo = c.From
		}
		if c.To.After(hi) {
			hi = c.To
		}
	}

	// Охватывающий диапазон ($5, $6) — подсказка планировщику: из одного
	// джойна с unnest он диапазон по period для индекса не выведет.
	rows, err := r.db.Query(ctx, query, snapshotID, froms, tos, divisionID, lo, hi)
	if err != nil {
		return nil, fmt.Errorf("pnl facts for snapshot %d: %w", snapshotID, err)
	}
	defer rows.Close()

	var facts []pnl.Fact
	for rows.Next() {
		var f pnl.Fact
		if err = rows.Scan(&f.Col, &f.Item,
			&f.Division.ID, &f.Division.Name, &f.SubItem.ID, &f.SubItem.Name, &f.Sum); err != nil {
			return nil, fmt.Errorf("scan pnl fact: %w", err)
		}

		facts = append(facts, f)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("pnl facts for snapshot %d: %w", snapshotID, err)
	}

	return facts, nil
}
