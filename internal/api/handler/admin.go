package handler

import (
	"context"
	"errors"

	"github.com/guregu/null/v6"

	"investudy_bot/internal/api/oas"
	"investudy_bot/internal/model"
	"investudy_bot/internal/repository"
	"investudy_bot/internal/users"
)

// Права администратора проверяет middleware по префиксу /api/admin/, а не
// каждый обработчик здесь: новая админская ручка защищена тем, что легла
// под префикс, и забыть проверку в ней нельзя.

func (h *Handler) ListUsers(ctx context.Context, _ oas.ListUsersRequestObject) (oas.ListUsersResponseObject, error) {
	list, err := h.users.List(ctx)
	if err != nil {
		return nil, err
	}

	out := make(oas.ListUsers200JSONResponse, len(list))
	for i, u := range list {
		out[i] = userDTO(u)
	}

	return out, nil
}

func (h *Handler) CreateUser(ctx context.Context, req oas.CreateUserRequestObject) (oas.CreateUserResponseObject, error) {
	in := userInput(oas.UserUpdate{
		Login: req.Body.Login, TelegramId: req.Body.TelegramId, Username: req.Body.Username,
		Role: req.Body.Role, DivisionId: req.Body.DivisionId, IsAdmin: req.Body.IsAdmin, Blocked: req.Body.Blocked,
	})

	var password string
	if req.Body.Password != nil {
		password = *req.Body.Password
	}

	u, err := h.users.Create(ctx, in, password)

	var invalid users.ValidationError
	switch {
	case errors.As(err, &invalid):
		return oas.CreateUser400JSONResponse{BadRequestJSONResponse: problems(invalid)}, nil
	case errors.Is(err, repository.ErrConflict):
		return oas.CreateUser409JSONResponse{ConflictJSONResponse: conflictMsg()}, nil
	case err != nil:
		return nil, err
	}

	return oas.CreateUser201JSONResponse(userDTO(u)), nil
}

func (h *Handler) UpdateUser(ctx context.Context, req oas.UpdateUserRequestObject) (oas.UpdateUserResponseObject, error) {
	u, err := h.users.Update(ctx, mustUser(ctx), req.Id, userInput(*req.Body))

	var invalid users.ValidationError
	switch {
	case errors.As(err, &invalid):
		return oas.UpdateUser400JSONResponse{BadRequestJSONResponse: problems(invalid)}, nil
	case errors.Is(err, repository.ErrNotFound):
		return oas.UpdateUser404JSONResponse{NotFoundJSONResponse: oas.NotFoundJSONResponse{Message: "Пользователь не найден"}}, nil
	case errors.Is(err, repository.ErrConflict):
		return oas.UpdateUser409JSONResponse{ConflictJSONResponse: conflictMsg()}, nil
	case err != nil:
		return nil, err
	}

	return oas.UpdateUser200JSONResponse(userDTO(u)), nil
}

func (h *Handler) SetUserPassword(ctx context.Context, req oas.SetUserPasswordRequestObject) (oas.SetUserPasswordResponseObject, error) {
	err := h.users.SetPassword(ctx, req.Id, req.Body.Password)

	var invalid users.ValidationError
	switch {
	case errors.As(err, &invalid):
		return oas.SetUserPassword400JSONResponse{BadRequestJSONResponse: problems(invalid)}, nil
	case errors.Is(err, repository.ErrNotFound):
		return oas.SetUserPassword404JSONResponse{NotFoundJSONResponse: oas.NotFoundJSONResponse{Message: "Пользователь не найден"}}, nil
	case err != nil:
		return nil, err
	}

	return oas.SetUserPassword204Response{}, nil
}

func (h *Handler) ListDivisions(ctx context.Context, _ oas.ListDivisionsRequestObject) (oas.ListDivisionsResponseObject, error) {
	list, err := h.divisions.Divisions(ctx)
	if err != nil {
		return nil, err
	}

	out := make(oas.ListDivisions200JSONResponse, len(list))
	for i, d := range list {
		out[i] = oas.Ref{Id: d.ID, Name: d.Name}
	}

	return out, nil
}

func problems(e users.ValidationError) oas.BadRequestJSONResponse {
	p := e.Problems
	return oas.BadRequestJSONResponse{Message: "Проверьте данные пользователя", Problems: &p}
}

func conflictMsg() oas.ConflictJSONResponse {
	return oas.ConflictJSONResponse{Message: "Логин или Telegram-id уже заняты другим пользователем"}
}

func userInput(b oas.UserUpdate) model.UserInput {
	return model.UserInput{
		Login:      null.StringFromPtr(b.Login),
		TelegramID: null.IntFromPtr(b.TelegramId),
		Username:   null.StringFromPtr(b.Username),
		Role:       string(b.Role),
		DivisionID: int32Ptr(b.DivisionId),
		IsAdmin:    b.IsAdmin,
		Blocked:    b.Blocked,
	}
}

func int32Ptr(p *int32) null.Int {
	if p == nil {
		return null.Int{}
	}

	return null.IntFrom(int64(*p))
}

func userDTO(u model.User) oas.User {
	hasPassword := u.HasPassword

	return oas.User{
		Id:          u.ID,
		Login:       u.Login.Ptr(),
		TelegramId:  u.TelegramID.Ptr(),
		Username:    u.Username.Ptr(),
		Role:        oas.Role(u.Role),
		Division:    divisionRef(u),
		IsAdmin:     u.IsAdmin,
		HasPassword: &hasPassword,
		BlockedAt:   u.BlockedAt.Ptr(),
		LastSeenAt:  u.LastSeenAt.Ptr(),
		CreatedAt:   u.CreatedAt,
		UpdatedAt:   u.UpdatedAt,
	}
}
