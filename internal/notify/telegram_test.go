package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSend(t *testing.T) {
	var got struct {
		path string
		form url.Values
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got.path, got.form = r.URL.Path, r.PostForm

		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tg := &Telegram{http: srv.Client(), base: srv.URL, token: "secret-token", chatID: 42}

	if err := tg.Send(context.Background(), "всё сломалось"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := "/botsecret-token/sendMessage"; got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
	if v := got.form.Get("chat_id"); v != "42" {
		t.Errorf("chat_id = %q, want 42", v)
	}
	if v := got.form.Get("text"); v != "всё сломалось" {
		t.Errorf("text = %q", v)
	}
	// parse_mode не отправляется: текст едет плоским, без экранирования.
	if _, ok := got.form["parse_mode"]; ok {
		t.Error("parse_mode is set; the message must go as plain text")
	}
}

// Отказ Bot API обязан стать ошибкой с внятным поводом — и без токена в тексте:
// ошибка уходит в лог, а токен лежит в пути URL.
func TestSendReportsAPIFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
	}))
	defer srv.Close()

	tg := &Telegram{http: srv.Client(), base: srv.URL, token: "secret-token", chatID: 42}

	err := tg.Send(context.Background(), "текст")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("error %q does not carry the API description", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("error %q leaks the bot token", err)
	}
}

// Недоступный API тоже не должен утащить токен в лог: транспортные ошибки
// несут в себе URL целиком.
func TestSendHidesTokenOnTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // адрес занят никем

	tg := &Telegram{http: srv.Client(), base: srv.URL, token: "secret-token", chatID: 42}

	err := tg.Send(context.Background(), "текст")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("error %q leaks the bot token", err)
	}
}

func TestTruncate(t *testing.T) {
	// Обрезка по рунам, а не по байтам: кириллица занимает два байта на символ.
	if got := truncate(strings.Repeat("я", 10), 4); got != "яяяя…" {
		t.Errorf("truncate = %q, want %q", got, "яяяя…")
	}
	if got := truncate("коротко", 100); got != "коротко" {
		t.Errorf("truncate = %q, want unchanged", got)
	}
}
