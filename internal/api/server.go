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
	"sync"
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

// Server — приложение и его остановка.
//
// Каждый запрос получает контекст от base с таймаутом запроса: запросы к базе
// не висят дольше DB_TIMEOUT, а при остановке их можно прервать разом. Без
// этого незавершённый запрос пережил бы Listen (fiber возвращается из него,
// как только закрыт приёмник, не дожидаясь запросов), держал бы соединение,
// и pool.Close в main ждал бы его сколько угодно — пока Docker не убьёт
// процесс SIGKILL, — а сам запрос упал бы на закрытом пуле.
type Server struct {
	app     *fiber.App
	timeout time.Duration

	base  context.Context
	abort context.CancelFunc
	// inflight — запросы, которые ещё выполняются.
	inflight sync.WaitGroup
}

// abortWait — сколько ждать прерванные запросы после отмены их контекста.
// pgx на отмену шлёт Postgres CancelRequest и возвращается быстро; дольше
// ждать значило бы упереться в SIGKILL от Docker.
const abortWait = 2 * time.Second

// New собирает приложение. requestTimeout — потолок одного запроса (DB_TIMEOUT).
func New(h *handler.Handler, a *auth.Service, requestTimeout time.Duration) *Server {
	s := newServer(requestTimeout)
	app := s.app

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

	return s
}

// newServer — приложение с учётом запросов, но без маршрутов: на нём же
// тестируется остановка.
func newServer(requestTimeout time.Duration) *Server {
	base, abort := context.WithCancel(context.Background())

	s := &Server{timeout: requestTimeout, base: base, abort: abort}

	s.app = fiber.New(fiber.Config{
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
		// Остановка не закрывает keep-alive соединения сама (ShutdownWithContext),
		// поэтому простаивающее соединение не должно жить бесконечно.
		IdleTimeout: 60 * time.Second,
	})

	// Первым: всё, что ниже, работает уже в контексте запроса.
	s.app.Use(s.track)

	return s
}

// App — приложение fiber; нужно тестам (app.Test).
func (s *Server) App() *fiber.App { return s.app }

// track считает запрос незавершённым до его конца и даёт ему контекст с
// таймаутом, производный от base.
func (s *Server) track(c fiber.Ctx) error {
	s.inflight.Add(1)
	defer s.inflight.Done()

	ctx, cancel := context.WithTimeout(s.base, s.timeout)
	defer cancel()

	c.SetContext(ctx)

	return c.Next()
}

// Run слушает addr до отмены ctx и останавливается штатно (см. serve).
func (s *Server) Run(ctx context.Context, addr string, grace time.Duration) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	return s.serve(ctx, ln, grace)
}

// serve обслуживает ln до отмены ctx. Остановка:
//
//  1. приёмник закрывается — новых запросов нет;
//  2. начатые запросы получают grace на то, чтобы закончиться самим;
//  3. оставшиеся прерываются отменой контекста — запросы к базе отменяются;
//  4. возврат — только когда запросов не осталось (или вышел abortWait),
//     и лишь после этого main закрывает пул.
func (s *Server) serve(ctx context.Context, ln net.Listener, grace time.Duration) error {
	defer s.abort()

	s.logRoutes()
	logger.INF("api started", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() { errCh <- s.app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	logger.INF("api stopping", "grace", grace)

	graceCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()

	if err := s.app.ShutdownWithContext(graceCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		logger.ERROR("api shutdown", "err", err)
	}

	done := make(chan struct{})
	go func() {
		s.inflight.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-graceCtx.Done():
		logger.WRN("api: запросы не уложились в остановку, прерываю", "grace", grace)
		s.abort()

		select {
		case <-done:
		case <-time.After(abortWait):
			logger.ERROR("api: прерванные запросы не завершились", "wait", abortWait)
		}
	}

	logger.INF("api stopped")

	return nil
}

// logRoutes пишет в лог все маршруты: что именно слушает этот бинарник,
// видно сразу при старте, без чтения кода и контракта.
func (s *Server) logRoutes() {
	routes := s.app.GetRoutes(true)

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
