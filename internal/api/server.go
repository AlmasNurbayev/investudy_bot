// Package api — транспорт REST API на fiber: маршруты, сессия, права,
// ошибки. Обработчики — internal/api/handler, контракт — api/openapi.yaml.
package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
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
//
// Документация открыта намеренно: контракт описывает форму запросов и секретов
// не содержит, а страницы входа на сайте до SPA нет — за сессией Swagger UI
// не из чего было бы открыть. «Try it out» после входа работает с cookie.
var public = map[string]bool{
	"/api/auth/login":   true,
	"/healthz":          true,
	"/api/docs":         true,
	"/api/openapi.yaml": true,
}

const adminPrefix = "/api/admin/"

// New собирает приложение. requestTimeout — потолок одного запроса (DB_TIMEOUT).
func New(h *handler.Handler, a *auth.Service, requestTimeout time.Duration) *fiber.App {
	app := newApp(requestTimeout)

	app.Use(recoverer.New())
	app.Use(accessLog)
	app.Use(session(h, a))

	app.Get("/healthz", func(c fiber.Ctx) error { return c.SendString("ok") })

	// Контракт и Swagger UI из того же файла, по которому сгенерирован сервер.
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

// newApp — приложение с настройками и контекстом запроса, но без маршрутов:
// на нём же тестируется остановка.
func newApp(requestTimeout time.Duration) *fiber.App {
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
		// Документация Shutdown*: keep-alive соединения остановка сама не
		// закрывает, и таймаут чтения рекомендуется ненулевой.
		IdleTimeout: 60 * time.Second,
	})

	// Контекст всех запросов отменяется в начале остановки — хуком fiber
	// OnPreShutdown, который ShutdownWithTimeout вызывает первым делом.
	// Свой контекст, а не c.RequestCtx(): fasthttp тоже отменяет его при
	// остановке, но как родитель context.WithTimeout он небезопасен — его
	// значения читает горутина отмены, пока fiber в них пишет (детектор
	// гонок это ловит).
	shutdown, cancel := context.WithCancel(context.Background())
	app.Hooks().OnPreShutdown(func() error {
		cancel()
		return nil
	})

	// Первым: всё, что ниже, работает уже в контексте запроса.
	app.Use(requestContext(shutdown, requestTimeout))

	return app
}

// requestContext даёт обработчикам контекст запроса: отменяется остановкой
// сервера и ограничен таймаутом DB_TIMEOUT. Через него pgx и отменяет
// запросы к базе: без этого (c.Context() по умолчанию пустой) запрос к базе
// не замечал ни остановки, ни таймаута и висел сколько угодно.
func requestContext(shutdown context.Context, timeout time.Duration) fiber.Handler {
	return func(c fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(shutdown, timeout)
		defer cancel()

		c.SetContext(ctx)

		return c.Next()
	}
}

// Run слушает addr до отмены ctx и останавливается средствами fiber.
func Run(ctx context.Context, app *fiber.App, addr string, grace time.Duration) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	return serve(ctx, app, ln, grace)
}

// serve — по документации fiber: Listen в горутине, по сигналу —
// ShutdownWithTimeout, и дождаться его. Он закрывает приёмник, отменяет
// контексты запросов (запросы к базе прерываются), ждёт, пока запросы
// допишут ответы, и через grace рвёт оставшиеся соединения.
//
// Ждать его обязательно: Listen возвращается, как только закрыт приёмник,
// не дожидаясь запросов, и pool.Close в main иначе закрыл бы пул под ними.
func serve(ctx context.Context, app *fiber.App, ln net.Listener, grace time.Duration) error {
	logRoutes(app)
	logger.INF("api started", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() { errCh <- app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	logger.INF("api stopping", "grace", grace)

	if err := app.ShutdownWithTimeout(grace); err != nil {
		logger.WRN("api: соединения закрыты принудительно", "grace", grace, "err", err)
	}

	<-errCh

	logger.INF("api stopped")

	return nil
}

// logRoutes пишет в лог все маршруты: что именно слушает этот бинарник,
// видно сразу при старте, без чтения кода и контракта.
func logRoutes(app *fiber.App) {
	routes := app.GetRoutes(true)

	slices.SortFunc(routes, func(a, b fiber.Route) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Method, b.Method))
	})

	for _, r := range routes {
		// HEAD fiber заводит сам к каждому GET — в логе это шум.
		if r.Method == fiber.MethodHead {
			continue
		}

		logger.INF("route", "method", r.Method, "path", r.Path, "access", routeAccess(r.Path))
	}
}

// routeAccess — кому доступен маршрут, по тем же правилам, что в session.
func routeAccess(path string) string {
	switch {
	case public[path]:
		return "public"
	case strings.HasPrefix(path, adminPrefix):
		return "admin"
	}

	return "session"
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

		user, cookieTTL, err := a.Authenticate(ctx, token, time.Now())
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
		// выбросил бы её по истечении срока после входа при живой сессии.
		if cookieTTL > 0 {
			c.Append(fiber.HeaderSetCookie, handler.SessionCookie(token, h.Secure(), cookieTTL))
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

	// Отмена и таймаут — не сбой кода: запрос прервала остановка сервера
	// или он упёрся в DB_TIMEOUT. Клиенту — что повторить, в лог — без ERROR.
	switch {
	case errors.Is(err, context.Canceled):
		logger.WRN("api: запрос прерван остановкой", "method", c.Method(), "path", c.Path())
		return c.Status(fiber.StatusServiceUnavailable).JSON(oas.Error{Message: "Сервер перезапускается, повторите запрос"})

	case errors.Is(err, context.DeadlineExceeded):
		logger.WRN("api: запрос не уложился в таймаут", "method", c.Method(), "path", c.Path(), "err", err)
		return c.Status(fiber.StatusGatewayTimeout).JSON(oas.Error{Message: "Запрос выполнялся слишком долго, повторите позже"})
	}

	logger.ERROR("api", "method", c.Method(), "path", c.Path(), "err", err)

	return c.Status(fiber.StatusInternalServerError).JSON(oas.Error{Message: "Внутренняя ошибка сервера"})
}

func accessLog(c fiber.Ctx) error {
	start := time.Now()
	err := c.Next()

	status := c.Response().StatusCode()
	if err != nil {
		status = errorStatus(err)
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

// errorStatus — код, которым errorHandler ответит на err: access-лог
// пишется раньше, чем ответ собран.
func errorStatus(err error) int {
	var fe *fiber.Error

	switch {
	case errors.As(err, &fe):
		return fe.Code
	case errors.Is(err, context.Canceled):
		return fiber.StatusServiceUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		return fiber.StatusGatewayTimeout
	}

	return fiber.StatusInternalServerError
}
