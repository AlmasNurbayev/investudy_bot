// Package api — транспорт REST API на fiber: маршруты, сессия, права,
// ошибки. Обработчики — internal/api/handler, контракт — api/openapi.yaml.
package api

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	recoverer "github.com/gofiber/fiber/v3/middleware/recover"

	apispec "investudy_bot/api"
	"investudy_bot/internal/api/handler"
	"investudy_bot/internal/api/oas"
	"investudy_bot/internal/auth"
	"investudy_bot/internal/logger"
)

// public — маршруты без сессии. Список, а не пометка на маршруте: по
// умолчанию закрыто всё, и новая ручка без сессии не откроется по забывчивости.
var public = map[string]bool{
	"/api/auth/login": true,
	"/healthz":        true,
}

const adminPrefix = "/api/admin/"

// New собирает приложение.
func New(h *handler.Handler, a *auth.Service) *fiber.App {
	app := fiber.New(fiber.Config{
		ErrorHandler: errorHandler,
		// Маршрут совпадает только с путём буква в букву. Иначе /API/admin/users
		// дошёл бы до админской ручки мимо проверки префикса /api/admin/ в
		// session — права проверяются по пути, и путь обязан быть одним.
		CaseSensitive: true,
		StrictRouting: true,
		// За nginx адрес клиента приходит заголовком. Верить ему можно только
		// от своего прокси (loopback и частные сети compose), иначе любой
		// подставил бы чужой адрес и обошёл ограничение попыток входа.
		TrustProxy:       true,
		TrustProxyConfig: fiber.TrustProxyConfig{Loopback: true, Private: true},
		ProxyHeader:      "X-Real-IP",
	})

	app.Use(recoverer.New())
	app.Use(accessLog)
	app.Use(session(h, a))

	app.Get("/healthz", func(c fiber.Ctx) error { return c.SendString("ok") })

	// Контракт и Swagger UI из того же файла, по которому сгенерирован
	// сервер — за сессией, как и всё остальное.
	app.Get("/api/openapi.yaml", func(c fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, "application/yaml; charset=utf-8")
		return c.Send(apispec.OpenAPI)
	})
	app.Get("/api/docs", func(c fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		return c.SendString(swaggerUI)
	})

	oas.RegisterHandlers(app, oas.NewStrictHandler(h, nil))

	return app
}

// session — проверка сессии и прав. Кладёт в контекст пользователя и
// сведения о клиенте; без сессии отвечает 401, без прав на /api/admin/ — 403.
func session(h *handler.Handler, a *auth.Service) fiber.Handler {
	return func(c fiber.Ctx) error {
		token := c.Cookies(handler.CookieName)

		ctx := handler.WithClient(c.Context(), handler.Client{
			IP:        c.IP(),
			UserAgent: c.Get(fiber.HeaderUserAgent),
			Token:     token,
		})
		c.SetContext(ctx)

		if public[c.Path()] {
			return c.Next()
		}

		user, renewed, err := a.Authenticate(ctx, token, time.Now())
		if errors.Is(err, auth.ErrNoSession) {
			return c.Status(fiber.StatusUnauthorized).JSON(oas.Error{Message: "Войдите заново"})
		}
		if err != nil {
			return err
		}

		if strings.HasPrefix(c.Path(), adminPrefix) && !user.IsAdmin {
			return c.Status(fiber.StatusForbidden).JSON(oas.Error{Message: "Нужны права администратора"})
		}

		// Срок сессии в базе продлён — продлеваем и cookie, иначе браузер
		// выбросил бы её через 90 дней после входа при живой сессии.
		if renewed {
			c.Append(fiber.HeaderSetCookie, handler.SessionCookie(token, h.Secure()))
		}

		c.SetContext(auth.WithUser(ctx, user))

		return c.Next()
	}
}

// errorHandler отвечает JSON-ом контракта на всё, что не разобрал
// обработчик: ошибки разбора запроса (400 от сгенерированного кода) и сбои.
// Текст сбоя клиенту не уходит — он в логе.
func errorHandler(c fiber.Ctx, err error) error {
	var fe *fiber.Error
	if errors.As(err, &fe) {
		return c.Status(fe.Code).JSON(oas.Error{Message: fe.Message})
	}

	logger.ERROR("api", "method", c.Method(), "path", c.Path(), "err", err)

	return c.Status(fiber.StatusInternalServerError).JSON(oas.Error{Message: "Внутренняя ошибка сервера"})
}

func accessLog(c fiber.Ctx) error {
	start := time.Now()
	err := c.Next()

	status := c.Response().StatusCode()
	if err != nil {
		var fe *fiber.Error
		if errors.As(err, &fe) {
			status = fe.Code
		} else {
			status = fiber.StatusInternalServerError
		}
	}

	logger.INF("http", "method", c.Method(), "path", c.Path(), "status", status, "took", time.Since(start).Round(time.Millisecond))

	return err
}

// swaggerUI — страница документации; сам UI — с CDN, контракт — свой.
const swaggerUI = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<title>Investudy API</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
<div id="ui"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>SwaggerUIBundle({url: "/api/openapi.yaml", dom_id: "#ui", withCredentials: true});</script>
</body>
</html>`
