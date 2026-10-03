package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

// start запускает приложение на свободном порту; stop отменяет контекст
// сервера, done получает результат serve.
func start(t *testing.T, app *fiber.App, grace time.Duration) (base string, stop context.CancelFunc, done <-chan error) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)

	go func() { ch <- serve(ctx, app, ln, grace) }()

	base = "http://" + ln.Addr().String()

	// Дождаться, пока сервер начнёт отвечать.
	for range 50 {
		if resp, err := client.Get(base + "/ping"); err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	return base, cancel, ch
}

// client без keep-alive. Go-клиент иногда заранее открывает лишнее
// соединение и не шлёт по нему запроса; fasthttp считает свежее соединение
// занятым ещё 5 секунд, и остановка ждала бы его весь grace. В бою такое
// соединение (спекулятивное у браузера) тоже ограничено grace и закрывается
// принудительно — безвредно, но для проверки времени остановки шумит.
var client = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

func get(url string) (int, error) {
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)

	return resp.StatusCode, nil
}

func ping(c fiber.Ctx) error { return c.SendString("pong") }

func wait(t *testing.T, done <-chan error, limit time.Duration, msg string) {
	t.Helper()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(limit):
		t.Fatal(msg)
	}
}

// Запрос, начатый до остановки, получает свой ответ, а serve возвращается
// только после него.
func TestShutdownWaitsForInflight(t *testing.T) {
	app := newApp(time.Minute)
	app.Get("/ping", ping)
	app.Get("/slow", func(c fiber.Ctx) error {
		time.Sleep(500 * time.Millisecond)
		return c.SendString("done")
	})

	base, stop, done := start(t, app, 3*time.Second)

	status := make(chan int, 1)
	go func() {
		code, _ := get(base + "/slow")
		status <- code
	}()

	time.Sleep(100 * time.Millisecond)
	stop()

	wait(t, done, 2*time.Second, "serve ждёт дольше, чем выполняется начатый запрос")

	select {
	case code := <-status:
		if code != http.StatusOK {
			t.Errorf("начатый запрос получил %d, ждали 200", code)
		}
	case <-time.After(time.Second):
		t.Error("начатый до остановки запрос так и не получил ответа")
	}
}

// Остановка отменяет контекст запроса — так прерывается и запрос к базе, —
// и клиент получает 503, а не обрыв. Ждать grace ради такого запроса незачем.
func TestShutdownCancelsRequestContext(t *testing.T) {
	app := newApp(time.Minute)

	var canceled atomic.Bool

	app.Get("/ping", ping)
	app.Get("/stuck", func(c fiber.Ctx) error {
		<-c.Context().Done()
		canceled.Store(errors.Is(c.Context().Err(), context.Canceled))

		return c.Context().Err()
	})

	base, stop, done := start(t, app, 3*time.Second)

	status := make(chan int, 1)
	go func() {
		code, _ := get(base + "/stuck")
		status <- code
	}()

	time.Sleep(100 * time.Millisecond)
	stop()

	wait(t, done, time.Second, "serve ждёт grace, хотя контекст запроса отменён")

	if !canceled.Load() {
		t.Error("остановка не отменила контекст запроса")
	}

	select {
	case code := <-status:
		if code != http.StatusServiceUnavailable {
			t.Errorf("прерванный запрос: %d, ждали 503", code)
		}
	case <-time.After(time.Second):
		t.Error("прерванный запрос не получил ответа")
	}
}

// Каждый запрос ограничен таймаутом: запрос к базе не висит дольше DB_TIMEOUT.
func TestRequestTimeout(t *testing.T) {
	app := newApp(200 * time.Millisecond)

	var err atomic.Value

	app.Get("/ping", ping)
	app.Get("/wait", func(c fiber.Ctx) error {
		<-c.Context().Done()
		err.Store(c.Context().Err())

		return c.Context().Err()
	})

	base, stop, done := start(t, app, time.Second)
	defer func() { stop(); <-done }()

	began := time.Now()
	code, e := get(base + "/wait")
	if e != nil {
		t.Fatal(e)
	}
	if code != http.StatusGatewayTimeout {
		t.Errorf("истёкший таймаут: %d, ждали 504", code)
	}

	if got, _ := err.Load().(error); !errors.Is(got, context.DeadlineExceeded) {
		t.Errorf("контекст запроса: %v, ждали DeadlineExceeded", got)
	}
	if took := time.Since(began); took > time.Second {
		t.Errorf("таймаут запроса не сработал: %s", took)
	}
}

func TestRouteAccess(t *testing.T) {
	cases := map[string]string{
		"/api/auth/login":   "public",
		"/healthz":          "public",
		"/api/docs":         "public",
		"/api/openapi.yaml": "public",
		"/api/admin/users":  "admin",
		"/api/pnl":          "session",
	}

	for path, want := range cases {
		if got := routeAccess(path); got != want {
			t.Errorf("%s: %s, ждали %s", path, got, want)
		}
	}
}
