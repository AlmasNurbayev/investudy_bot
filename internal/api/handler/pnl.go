package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"investudy_bot/internal/api/oas"
	"investudy_bot/internal/lib/money"
	"investudy_bot/internal/lib/period"
	"investudy_bot/internal/lib/snapshot"
	"investudy_bot/internal/pnl"
	"investudy_bot/internal/report"
)

// snapshotsListed — сколько версий отдавать в селектор.
const snapshotsListed = 50

const noRole = "Роль не назначена — отчёт недоступен. Обратитесь к администратору."

func (h *Handler) ListSnapshots(ctx context.Context, _ oas.ListSnapshotsRequestObject) (oas.ListSnapshotsResponseObject, error) {
	list, current, err := h.reports.Snapshots(ctx, snapshotsListed)
	if err != nil {
		return nil, err
	}

	out := oas.ListSnapshots200JSONResponse{Items: make([]oas.Snapshot, 0, len(list))}
	if current.ID != 0 {
		out.CurrentId = &current.ID
	}

	for _, s := range list {
		out.Items = append(out.Items, oas.Snapshot{Id: s.ID, TakenAt: s.TakenAt.Time, RowCount: s.RowCount.Int64})
	}

	return out, nil
}

func (h *Handler) GetPnl(ctx context.Context, req oas.GetPnlRequestObject) (oas.GetPnlResponseObject, error) {
	pol, err := policy(ctx)
	if err != nil {
		return oas.GetPnl400JSONResponse{BadRequestJSONResponse: oas.BadRequestJSONResponse{Message: noRole}}, nil
	}

	grain := period.Month
	if req.Params.Grain != nil {
		grain = period.Grain(*req.Params.Grain)
	}

	limits, err := h.reports.PnlLimits(ctx)
	if err != nil {
		return nil, err
	}

	var cols []period.Column
	if req.Params.Cols == nil || len(*req.Params.Cols) == 0 {
		if grain != period.Month {
			return badRequest("для кварталов и лет выберите колонки явно"), nil
		}

		cols = period.DefaultColumns(h.now(), limits.DefaultMonths)
	} else if cols, err = period.ParseColumns(grain, *req.Params.Cols, limits.MaxColumns); err != nil {
		return badRequest(err.Error()), nil
	}

	var snapshotID int64
	if req.Params.Snapshot != nil {
		snapshotID = *req.Params.Snapshot
	}

	res, err := h.reports.PnL(ctx, pol, snapshotID, cols)
	switch {
	case errors.Is(err, report.ErrSnapshotNotFound):
		return oas.GetPnl404JSONResponse{NotFoundJSONResponse: oas.NotFoundJSONResponse{Message: err.Error()}}, nil
	case errors.Is(err, snapshot.ErrNoSnapshot):
		return oas.GetPnl404JSONResponse{NotFoundJSONResponse: oas.NotFoundJSONResponse{
			Message: "Данных ещё нет: ни одной загрузки из Google Sheets."}}, nil
	case err != nil:
		return nil, err
	}

	return pnlReport(res, pol.ShowUnmapped, limits.MaxColumns), nil
}

func badRequest(msg string) oas.GetPnl400JSONResponse {
	return oas.GetPnl400JSONResponse{BadRequestJSONResponse: oas.BadRequestJSONResponse{Message: msg}}
}

// pnlReport переводит отчёт в DTO. Суммы неразмеченных статей уходят только
// при showUnmapped; флаг неполноты — всем.
func pnlReport(res report.PnL, showUnmapped bool, maxColumns int) oas.GetPnl200JSONResponse {
	out := oas.GetPnl200JSONResponse{
		MaxColumns: maxColumns,
		Snapshot: oas.SnapshotRef{
			Id:      res.Snapshot.ID,
			TakenAt: res.Snapshot.TakenAt.Time,
			Stale:   res.Stale,
		},
		Columns:    make([]oas.Column, len(res.Columns)),
		Lines:      make([]oas.PnlLine, len(res.Report.Lines)),
		Incomplete: len(res.Report.Unmapped) > 0,
	}

	for i, c := range res.Columns {
		out.Columns[i] = oas.Column{
			Key:   c.Key,
			Title: c.Title,
			From:  openapi_types.Date{Time: c.From},
			To:    openapi_types.Date{Time: c.To},
		}
	}

	for i, l := range res.Report.Lines {
		out.Lines[i] = oas.PnlLine{
			Code:     l.Code,
			Title:    l.Title,
			Kind:     oas.PnlLineKind(l.Kind),
			Values:   values(l.Values),
			Children: nodes(l.Children),
		}
	}

	if showUnmapped {
		unmapped := make([]oas.PnlUnmapped, len(res.Report.Unmapped))
		for i, u := range res.Report.Unmapped {
			unmapped[i] = oas.PnlUnmapped{Item: u.Item, Values: values(u.Values)}
		}

		out.Unmapped = &unmapped
	}

	return out
}

func nodes(in []pnl.Node) *[]oas.PnlNode {
	if len(in) == 0 {
		return nil
	}

	out := make([]oas.PnlNode, len(in))
	for i, n := range in {
		out[i] = oas.PnlNode{
			Dim:      oas.PnlNodeDim(n.Dim),
			Name:     n.Name,
			Values:   values(n.Values),
			Children: nodes(n.Children),
		}

		if n.ID != 0 {
			id := n.ID
			out[i].Id = &id
		}
	}

	return &out
}

func values(in []pgtype.Numeric) []oas.Money {
	out := make([]oas.Money, len(in))
	for i, v := range in {
		out[i] = money.Decimal(v)
	}

	return out
}
