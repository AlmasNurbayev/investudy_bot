package bot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	tg "github.com/go-telegram/bot"

	"investudy_bot/internal/bot/handler"
)

// Таблица команд кормит и маршрутизацию, и меню Telegram, но написание имени
// в них разное: RegisterHandler ждёт «/name», SetMyCommands — «name».
// Ошибка тихая — Telegram отвергнет весь список, и меню останется старым.
func TestCommandsAreWellFormed(t *testing.T) {
	b := &Bot{commands: commands(handler.NewClosedReports(nil))}

	if len(b.commands) == 0 {
		t.Fatal("список команд пуст: меню окажется пустым, а команды — незарегистрированными")
	}

	for _, c := range b.commands {
		if !strings.HasPrefix(c.name, "/") {
			t.Errorf("%q: RegisterHandler не поймает команду без ведущей косой черты", c.name)
		}
		if c.description == "" {
			t.Errorf("%q: Telegram отвергает команду с пустым описанием", c.name)
		}
		if c.handle == nil {
			t.Errorf("%q: команда попадёт в меню, но не сработает", c.name)
		}
	}

	for _, m := range b.menu() {
		if strings.HasPrefix(m.Command, "/") {
			t.Errorf("%q: Telegram отвергает имя с косой чертой", m.Command)
		}
		if n := len(m.Command); n < 1 || n > 32 {
			t.Errorf("%q: длина имени %d вне допустимых 1..32", m.Command, n)
		}
		if strings.ContainsAny(m.Command, " /") {
			t.Errorf("%q: имя не должно содержать пробел или косую черту", m.Command)
		}
		if n := len([]rune(m.Description)); n < 1 || n > 256 {
			t.Errorf("%q: длина описания %d вне допустимых 1..256", m.Command, n)
		}
	}
}

// Меню обязано покрывать ровно то, что бот понимает: лишняя строка — кнопка,
// на которую бот ответит «не знаю такой команды».
func TestMenuMatchesCommands(t *testing.T) {
	b := &Bot{commands: commands(handler.NewClosedReports(nil))}

	menu := b.menu()
	if len(menu) != len(b.commands) {
		t.Fatalf("в меню %d команд, зарегистрировано %d", len(menu), len(b.commands))
	}

	for i, c := range b.commands {
		if want := strings.TrimPrefix(c.name, "/"); menu[i].Command != want {
			t.Errorf("меню[%d] = %q, want %q", i, menu[i].Command, want)
		}
	}
}

// Проверяет то, что нельзя увидеть по типам: как запрос выглядит на проводе
// и в какие области уходит.
//
// Telegram отвергает имя с ведущей косой чертой и требует у области поле type,
// которое появляется только через MarshalCustom. Область важна не меньше:
// он берёт первый установленный список в порядке chat → all_private_chats →
// default, поэтому публикация в одну только default перекрывается чем угодно
// выше, включая пустой список. Обе ошибки обнаружились бы иначе лишь тем, что
// меню в клиенте молча осталось прежним, без ошибки в логе.
func TestPublishCommandsRequest(t *testing.T) {
	var (
		mu    sync.Mutex
		sent  []string
		types []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/setMyCommands") {
			t.Errorf("неожиданный метод API: %s", r.URL.Path)
		}

		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		sent = append(sent, string(body))
		for _, scope := range []string{"all_private_chats", "default"} {
			if strings.Contains(string(body), `"type":"`+scope+`"`) {
				types = append(types, scope)
			}
		}
		mu.Unlock()

		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer srv.Close()

	api, err := tg.New("test-token", tg.WithServerURL(srv.URL), tg.WithSkipGetMe())
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	b := &Bot{api: api, commands: commands(handler.NewClosedReports(nil))}
	b.publishCommands(context.Background())

	if len(sent) != 2 {
		t.Fatalf("отправлено %d запросов, want 2 (по области на каждый)", len(sent))
	}

	sort.Strings(types)
	if want := []string{"all_private_chats", "default"}; !slices.Equal(types, want) {
		t.Errorf("области = %v, want %v", types, want)
	}

	for _, body := range sent {
		if !strings.Contains(body, `"command":"closed_reports"`) {
			t.Errorf("в запросе нет команды:\n%s", body)
		}
		if strings.Contains(body, `"command":"/`) {
			t.Errorf("имя ушло с ведущей косой чертой — Telegram отвергнет список:\n%s", body)
		}
	}
}
