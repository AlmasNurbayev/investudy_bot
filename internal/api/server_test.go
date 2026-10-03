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

// start запускает сервер на свободном порту; stop отменяет его контекст,
// done закрывается, когда serve вернулся.
func start(t *testing.T, s *Server, grace time.Duration) (base string, stop context.CancelFunc, done <-chan error) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)

	go func() { ch <- s.serve(ctx, ln, grace) }()

	base = "http://" + ln.Addr().String()

	// Дождаться, пока сервер начнёт отвечать.
	for range 50 {
		if resp, err := http.Get(base + "/ping"); err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	return base, cancel, ch
}

func get(url string) (int, error) {
	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)

	return resp.StatusCode, nil
}

func ping(c fiber.Ctx) error { return c.SendString("pong") }

// Запрос, начатый до остановки и уложившийся в grace, получает свой ответ,
// а serve возвращается только после него.
func TestShutdownWaitsForInflight(t *testing.T) {
	s := newServer(time.Minute)
	s.app.Get("/ping", ping)
	s.app.Get("/slow", func(c fiber.Ctx) error {
		time.Sleep(500 * time.Millisecond)
		return c.SendString("done")
	})

	base, stop, done := start(t, s, 3*time.Second)

	status := make(chan int, 1)
	go func() {
		code, _ := get(base + "/slow")
		status <- code
	}()

	time.Sleep(100 * time.Millisecond)
	stop()

	// Ответ на /slow — через 400 мс после остановки; serve обязан вернуться
	// сразу за ним, а не ждать весь grace из-за keep-alive соединений.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve ждёт дольше, чем выполняется начатый запрос: держат keep-alive соединения?")
	}

	// Ответ обязан дойти целиком, а не оборваться остановкой. Ждём его с
	// запасом: клиентской горутине ещё нужно дочитать тело.
	select {
	case code := <-status:
		if code != http.StatusOK {
			t.Errorf("начатый запрос получил %d, ждали 200", code)
		}
	case <-time.After(time.Second):
		t.Error("начатый до остановки запрос так и не получил ответа")
	}
}

// Запрос, не уложившийся в grace, прерывается отменой контекста — так
// отменяется и запрос к базе, — и serve возвращается за grace + abortWait,
// а не ждёт его вечно.
func TestShutdownAbortsStuckRequest(t *testing.T) {
	s := newServer(time.Minute)

	var sawCancel, finished atomic.Bool

	s.app.Get("/ping", ping)
	s.app.Get("/stuck", func(c fiber.Ctx) error {
		<-c.Context().Done()
		sawCancel.Store(errors.Is(c.Context().Err(), context.Canceled))

		// Отмена запроса к базе — не мгновенная: pgx шлёт Postgres
		// CancelRequest и ждёт ответа. serve обязан дождаться и этого,
		// иначе pool.Close в main снова упрётся в занятое соединение.
		time.Sleep(200 * time.Millisecond)
		finished.Store(true)

		return c.Context().Err()
	})

	const grace = 300 * time.Millisecond

	base, stop, done := start(t, s, grace)

	stuckStatus := make(chan int, 1)
	go func() {
		code, _ := get(base + "/stuck")
		stuckStatus <- code
	}()

	time.Sleep(100 * time.Millisecond)

	began := time.Now()
	stop()

	select {
	case <-done:
	case <-time.After(grace + abortWait + time.Second):
		t.Fatal("serve ждёт зависший запрос дольше grace + abortWait")
	}

	if took := time.Since(began); took < grace {
		t.Errorf("serve вернулся через %s — раньше grace", took)
	}

	if !sawCancel.Load() {
		t.Error("контекст зависшего запроса не отменён")
	}
	if !finished.Load() {
		t.Error("serve вернулся, не дождавшись прерванного запроса: пул закрылся бы под ним")
	}

	// Прерванный остановкой запрос — 503 «повторите», а не 500 «сбой».
	select {
	case code := <-stuckStatus:
		if code != http.StatusServiceUnavailable {
			t.Errorf("прерванный запрос: %d, ждали 503", code)
		}
	case <-time.After(time.Second):
		t.Error("прерванный запрос не получил ответа")
	}
}

// Каждый запрос ограничен таймаутом: запрос к базе не висит дольше DB_TIMEOUT.
func TestRequestTimeout(t *testing.T) {
	s := newServer(200 * time.Millisecond)

	var err atomic.Value

	s.app.Get("/ping", ping)
	s.app.Get("/wait", func(c fiber.Ctx) error {
		<-c.Context().Done()
		err.Store(c.Context().Err())

		return c.Context().Err()
	})

	base, stop, done := start(t, s, time.Second)
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
		"/api/auth/login":  "public",
		"/healthz":         "public",
		"/api/admin/users": "admin",
		"/api/pnl":         "session",
	}

	for path, want := range cases {
		if got := routeAccess(path); got != want {
			t.Errorf("%s: %s, ждали %s", path, got, want)
		}
	}
}
