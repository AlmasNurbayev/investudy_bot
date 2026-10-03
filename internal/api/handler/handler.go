// Package handler — обработчики REST API: реализация сгенерированного
// oas.StrictServerInterface и перевод доменных структур в DTO контракта.
//
// Всё, что видит клиент — коды ответов, тексты ошибок, форма JSON, — собрано
// здесь, как тексты бота лежат в internal/bot/handler: сервисы отдают данные
// и доменные ошибки, а не HTTP.
package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v3"

	"investudy_bot/internal/access"
	"investudy_bot/internal/api/oas"
	"investudy_bot/internal/auth"
	"investudy_bot/internal/logger"
	"investudy_bot/internal/model"
	"investudy_bot/internal/report"
	"investudy_bot/internal/users"
)

// CookieName — имя cookie сессии; то же имя стоит в securitySchemes контракта.
const CookieName = "session"

// Divisions — справочник подразделений (реализует repository.Users).
type Divisions interface {
	Divisions(ctx context.Context) ([]model.Ref, error)
}

type Handler struct {
	auth      *auth.Service
	users     *users.Service
	reports   *report.Service
	divisions Divisions
	// secure — флаг Secure у cookie. Выключается только для локального
	// запуска по http: браузер не вернёт Secure-cookie по открытому каналу.
	secure bool
	now    func() time.Time
}

var _ oas.StrictServerInterface = (*Handler)(nil)

func New(a *auth.Service, u *users.Service, r *report.Service, d Divisions, secureCookie bool) *Handler {
	return &Handler{auth: a, users: u, reports: r, divisions: d, secure: secureCookie, now: time.Now}
}

// Client — сведения о запросе, нужные обработчикам помимо тела: адрес для
// ограничения попыток входа, агент для списка сессий, токен для выхода.
// Кладёт их в контекст middleware: строгие обработчики fiber.Ctx не видят.
type Client struct {
	IP        string
	UserAgent string
	Token     string
}

type clientKey struct{}

func WithClient(ctx context.Context, c Client) context.Context {
	return context.WithValue(ctx, clientKey{}, c)
}

func clientFrom(ctx context.Context) Client {
	c, _ := ctx.Value(clientKey{}).(Client)
	return c
}

// SessionCookie — cookie сессии. HttpOnly — токен не виден JS;
// SameSite=Lax — чужой сайт не отправит его фоновым POST. Срок совпадает
// со скользящим сроком сессии и продлевается вместе с ним.
func SessionCookie(token string, secure bool) string {
	c := http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(auth.SessionTTL / time.Second),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}

	return c.String()
}

// Secure — флаг Secure у cookie: middleware продлевает cookie тем же способом.
func (h *Handler) Secure() bool { return h.secure }

func (h *Handler) Login(ctx context.Context, req oas.LoginRequestObject) (oas.LoginResponseObject, error) {
	c := clientFrom(ctx)

	token, user, err := h.auth.Login(ctx, req.Body.Login, req.Body.Password, c.IP, c.UserAgent, h.now())

	var tooMany auth.TooManyAttempts
	switch {
	case errors.As(err, &tooMany):
		retry := int(tooMany.RetryAfter / time.Second)

		return oas.Login429JSONResponse{
			Body:    oas.Error{Message: tooMany.Error()},
			Headers: oas.Login429ResponseHeaders{RetryAfter: &retry},
		}, nil

	case errors.Is(err, auth.ErrBadCredentials):
		return oas.Login401JSONResponse{UnauthorizedJSONResponse: oas.UnauthorizedJSONResponse{Message: err.Error()}}, nil

	case err != nil:
		return nil, err
	}

	cookie := SessionCookie(token, h.secure)

	return oas.Login200JSONResponse{
		Body:    meDTO(user),
		Headers: oas.Login200ResponseHeaders{SetCookie: &cookie},
	}, nil
}

func (h *Handler) Logout(ctx context.Context, _ oas.LogoutRequestObject) (oas.LogoutResponseObject, error) {
	if err := h.auth.Logout(ctx, clientFrom(ctx).Token); err != nil {
		return nil, err
	}

	return logoutResponse{secure: h.secure}, nil
}

// logoutResponse гасит cookie в браузере: сгенерированный 204 заголовков
// не несёт, а без сброса браузер слал бы мёртвый токен до истечения срока.
type logoutResponse struct{ secure bool }

func (r logoutResponse) VisitLogoutResponse(c fiber.Ctx) error {
	cookie := http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: r.secure, SameSite: http.SameSiteLaxMode,
	}
	c.Response().Header.Set("Set-Cookie", cookie.String())
	c.Status(fiber.StatusNoContent)

	return nil
}

func (h *Handler) GetMe(ctx context.Context, _ oas.GetMeRequestObject) (oas.GetMeResponseObject, error) {
	return oas.GetMe200JSONResponse(meDTO(mustUser(ctx))), nil
}

// meDTO — пользователь и его граница доступа: ответ и входа, и /api/me.
func meDTO(u model.User) oas.Me {
	me := oas.Me{
		Id:       u.ID,
		Login:    u.Login.Ptr(),
		Username: u.Username.Ptr(),
		Role:     oas.Role(u.Role),
		IsAdmin:  u.IsAdmin,
		Division: divisionRef(u),
	}

	// Роль без политики (не назначена, неизвестна) — не повод не отвечать
	// на «кто я»: фронт покажет, что отчёт недоступен, а не ошибку входа.
	if p, err := access.For(policyUser(u)); err == nil {
		me.HasReport = true
		if p.LastLine != "" {
			me.LastLine = &p.LastLine
		}
	}

	return me
}

// policy — политика пользователя запроса. Ошибка — роли нет или она
// неизвестна: такому пользователю отчёт не показывается.
func policy(ctx context.Context) (access.Policy, error) {
	u := mustUser(ctx)

	p, err := access.For(policyUser(u))
	if err != nil {
		logger.WRN("no policy", "user", u.ID, "role", u.Role, "err", err)
	}

	return p, err
}

func policyUser(u model.User) access.User {
	return access.User{
		Role:       access.Role(u.Role),
		DivisionID: int32(u.DivisionID.Int64),
		IsAdmin:    u.IsAdmin,
	}
}

// mustUser — пользователь запроса. Его кладёт middleware сессии, и без него
// запрос до обработчика не доходит; паника здесь — ошибка сборки маршрутов,
// а не ввод пользователя, и её поймает recover.
func mustUser(ctx context.Context) model.User {
	u, ok := auth.UserFrom(ctx)
	if !ok {
		panic("handler: запрос без пользователя прошёл мимо middleware сессии")
	}

	return u
}

func divisionRef(u model.User) *oas.Ref {
	if !u.DivisionID.Valid {
		return nil
	}

	return &oas.Ref{Id: int32(u.DivisionID.Int64), Name: u.DivisionName.String}
}
