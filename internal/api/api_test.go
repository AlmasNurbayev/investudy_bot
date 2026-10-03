package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/guregu/null/v6"
	"github.com/jackc/pgx/v5/pgxpool"

	"investudy_bot/internal/access"
	"investudy_bot/internal/api"
	"investudy_bot/internal/api/handler"
	"investudy_bot/internal/api/oas"
	"investudy_bot/internal/auth"
	"investudy_bot/internal/model"
	"investudy_bot/internal/report"
	"investudy_bot/internal/repository"
	"investudy_bot/internal/users"
)

// Тест требует пустой БД с накатанными миграциями, как и тесты репозитория:
//
//	make test-integration TEST_DATABASE_URL=postgres://...
//
// Без переменной пропускается.

const password = "long enough password"

type env struct {
	t     *testing.T
	app   *fiber.App
	pool  *pgxpool.Pool
	users *users.Service
	sales int64
}

func setup(t *testing.T) *env {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err = pool.Exec(ctx, `TRUNCATE snapshots, divisions, items, sub_items, fin_types, vids, users CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	publish(t, pool)

	store := repository.NewUsers(pool)

	authSvc, err := auth.New(store)
	if err != nil {
		t.Fatal(err)
	}

	usersSvc := users.New(store)
	h := handler.New(authSvc, usersSvc, report.New(repository.NewReader(pool)), store, false)

	e := &env{t: t, app: api.New(h, authSvc, 10*time.Second), pool: pool, users: usersSvc}

	if err = pool.QueryRow(ctx, `SELECT id FROM divisions WHERE name = 'отдел продаж'`).Scan(&e.sales); err != nil {
		t.Fatalf("division: %v", err)
	}

	return e
}

func row(period string, sum float64, division, item, subItem string) model.Row {
	d, _ := time.Parse("02.01.2006", period)

	return model.Row{
		Date: null.TimeFrom(d), Period: null.TimeFrom(d), SumDash: null.FloatFrom(sum),
		Division: division, Item: item, SubItem: subItem,
	}
}

func publish(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	ctx := context.Background()

	tx, err := repository.NewStore(pool).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id, err := tx.BeginSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	n, err := tx.InsertRows(ctx, id, []model.Row{
		row("01.03.2026", 1000, "отдел продаж", "доходы", "курсы"),
		row("01.03.2026", 700, "ВИ", "доходы", "курсы"),
		row("01.03.2026", -300, "отдел продаж", "произв. расходы", "аренда"),
		row("01.03.2026", -200, "ВИ", "произв. расходы", "аренда"),
		row("01.03.2026", -50, "руководство", "админ. расходы", "аренда"),
		row("01.03.2026", 20, "руководство", "прочие доходы", "проценты"),
		row("01.03.2026", -100, "руководство", "дивиденды", "учредитель"),
		row("01.03.2026", 5, "отдел продаж", "новая статья", ""),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err = tx.FinishSnapshot(ctx, id, n); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// user заводит пользователя с логином и паролем.
func (e *env) user(login string, role access.Role, division int64, admin bool) model.User {
	e.t.Helper()

	in := model.UserInput{Login: null.StringFrom(login), Role: string(role), IsAdmin: admin}
	if division != 0 {
		in.DivisionID = null.IntFrom(division)
	}

	u, err := e.users.Create(context.Background(), in, password)
	if err != nil {
		e.t.Fatalf("create %s: %v", login, err)
	}

	return u
}

// do выполняет запрос; cookie — значение cookie сессии или "".
func (e *env) do(method, path, cookie string, body any) *http.Response {
	e.t.Helper()

	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}

	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: handler.CookieName, Value: cookie})
	}

	resp, err := e.app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}

	return resp
}

func (e *env) login(login string) string {
	e.t.Helper()

	cookie, _ := e.loginAs(login)

	return cookie
}

// loginAs входит и возвращает cookie сессии вместе с телом ответа.
func (e *env) loginAs(login string) (string, oas.Me) {
	e.t.Helper()

	resp := e.do(http.MethodPost, "/api/auth/login", "", oas.LoginRequest{Login: login, Password: password})
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("login %s: %d %s", login, resp.StatusCode, read(resp))
	}

	me := decode[oas.Me](e.t, resp)

	for _, c := range resp.Cookies() {
		if c.Name == handler.CookieName {
			if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
				e.t.Errorf("cookie без HttpOnly/SameSite=Lax: %+v", c)
			}

			return c.Value, me
		}
	}

	e.t.Fatalf("login %s: cookie не выдана", login)

	return "", me
}

func read(resp *http.Response) string {
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()

	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}

	return v
}

func (e *env) pnl(cookie string) oas.PnlReport {
	e.t.Helper()

	resp := e.do(http.MethodGet, "/api/pnl?cols=2026-03", cookie, nil)
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("pnl: %d %s", resp.StatusCode, read(resp))
	}

	return decode[oas.PnlReport](e.t, resp)
}

func codes(r oas.PnlReport) string {
	out := make([]string, len(r.Lines))
	for i, l := range r.Lines {
		out[i] = l.Code
	}

	return strings.Join(out, ",")
}

// Граница роли: ни одной строки ниже, руководителю — только своё
// подразделение даже в раскрытии; неразмеченные — только администратору.
func TestPnlPolicyPerRole(t *testing.T) {
	e := setup(t)

	e.user("cfo", access.CFO, 0, true)
	e.user("coo", access.COO, 0, false)
	e.user("cco", access.CCO, 0, false)
	e.user("head", access.DivisionHead, e.sales, false)

	cases := []struct {
		login, codes, last string
		unmapped           bool
	}{
		{"cfo", "1.1,1.2,1.3,1,2,3,4,5.1,5.2,5.3,6,7.1,7.2,7.3,7.4,7.5,8", "1070.00", true},
		{"coo", "1.1,1.2,1.3,1,2,3,4", "1150.00", false},
		{"cco", "1.1,1.2,1.3,1,2,3,4,5.1,5.2,5.3,6", "1170.00", false},
		// Руководитель: только отдел продаж — 1000 - 300.
		{"head", "1.1,1.2,1.3,1", "700.00", false},
	}

	for _, c := range cases {
		r := e.pnl(e.login(c.login))

		if got := codes(r); got != c.codes {
			t.Errorf("%s: строки %s, ждали %s", c.login, got, c.codes)
		}
		if got := r.Lines[len(r.Lines)-1].Values[0]; got != c.last {
			t.Errorf("%s: последняя строка %s, ждали %s", c.login, got, c.last)
		}
		if !r.Incomplete {
			t.Errorf("%s: неразмеченная статья не подняла флаг неполноты", c.login)
		}
		if (r.Unmapped != nil) != c.unmapped {
			t.Errorf("%s: неразмеченные видны=%v, ждали %v", c.login, r.Unmapped != nil, c.unmapped)
		}
	}

	head := e.pnl(e.login("head"))
	for _, l := range head.Lines {
		if l.Children == nil {
			continue
		}

		for _, n := range *l.Children {
			if n.Dim == oas.Division && (n.Id == nil || int64(*n.Id) != e.sales) {
				t.Errorf("руководитель видит чужое подразделение %q в %s", n.Name, l.Code)
			}
		}
	}
}

// Срок сессии берётся из settings: меняем настройку — меняется и Max-Age
// cookie, и expires_at в базе, без перезапуска.
func TestSessionTTLFromSettings(t *testing.T) {
	e := setup(t)
	e.user("cfo", access.CFO, 0, true)

	ctx := context.Background()

	e.setSetting("auth", `{"session_ttl_days": 30, "login_max_failures": 5, "login_window_minutes": 15, "min_password_length": 10}`)

	resp := e.do(http.MethodPost, "/api/auth/login", "", oas.LoginRequest{Login: "cfo", Password: password})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, read(resp))
	}

	var maxAge int
	for _, c := range resp.Cookies() {
		if c.Name == handler.CookieName {
			maxAge = c.MaxAge
		}
	}
	if want := 30 * 24 * 3600; maxAge != want {
		t.Errorf("Max-Age = %d, ждали %d (30 дней)", maxAge, want)
	}

	var days float64
	err := e.pool.QueryRow(ctx, `SELECT extract(epoch FROM (expires_at - created_at)) / 86400 FROM sessions`).Scan(&days)
	if err != nil || days < 29.99 || days > 30.01 {
		t.Errorf("expires_at - created_at = %.2f дней, err=%v, ждали 30", days, err)
	}

	// Нуль в настройке — срок по умолчанию в 1 день, вход не ломается.
	e.setSetting("auth", `{"session_ttl_days": 0}`)
	resp = e.do(http.MethodPost, "/api/auth/login", "", oas.LoginRequest{Login: "cfo", Password: password})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session_ttl_days=0: %d, ждали 200", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == handler.CookieName && c.MaxAge != 24*3600 {
			t.Errorf("Max-Age = %d, ждали сутки", c.MaxAge)
		}
	}
}

// setSetting меняет настройку в общей базе и возвращает прежнее значение при
// выходе из теста: следующие тесты ждут сид миграции.
func (e *env) setSetting(key, value string) {
	e.t.Helper()

	ctx := context.Background()

	var old, description string
	err := e.pool.QueryRow(ctx, `SELECT value::text, coalesce(description, '') FROM settings WHERE key = $1`, key).
		Scan(&old, &description)
	if err != nil {
		e.t.Fatalf("read setting %s: %v", key, err)
	}

	// Upsert, а не UPDATE: тест может удалить строку вовсе (проверка «нет
	// настройки»), и восстановить её надо целиком, с описанием.
	e.t.Cleanup(func() {
		_, _ = e.pool.Exec(ctx, `
			INSERT INTO settings (key, value, description) VALUES ($1, $2, nullif($3, ''))
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, description = EXCLUDED.description`,
			key, old, description)
	})

	if _, err := e.pool.Exec(ctx, `UPDATE settings SET value = $2 WHERE key = $1`, key, value); err != nil {
		e.t.Fatalf("set setting %s: %v", key, err)
	}
}

// Потолок колонок и месяцы по умолчанию — из settings (ключ pnl), и потолок
// доезжает до фронта в ответе.
func TestPnlLimitsFromSettings(t *testing.T) {
	e := setup(t)
	e.user("cfo", access.CFO, 0, true)
	cookie := e.login("cfo")

	report := func(query string) (int, oas.PnlReport) {
		resp := e.do(http.MethodGet, "/api/pnl"+query, cookie, nil)
		if resp.StatusCode != http.StatusOK {
			return resp.StatusCode, oas.PnlReport{}
		}

		return resp.StatusCode, decode[oas.PnlReport](t, resp)
	}

	// Сид миграции: 12 колонок, 6 месяцев по умолчанию.
	if code, r := report(""); code != http.StatusOK || r.MaxColumns != 12 || len(r.Columns) != 6 {
		t.Fatalf("по умолчанию: %d, max_columns=%d, колонок %d", code, r.MaxColumns, len(r.Columns))
	}

	e.setSetting("pnl", `{"default_months": 3, "max_columns": 2}`)

	if code, r := report(""); code != http.StatusOK || r.MaxColumns != 2 || len(r.Columns) != 2 {
		t.Errorf("месяцев 3 при потолке 2 должно быть 2 колонки: %d, max_columns=%d, колонок %d", code, r.MaxColumns, len(r.Columns))
	}
	if code, _ := report("?cols=2026-01,2026-02"); code != http.StatusOK {
		t.Errorf("две колонки при потолке 2: %d", code)
	}
	if code, _ := report("?cols=2026-01,2026-02,2026-03"); code != http.StatusBadRequest {
		t.Errorf("три колонки при потолке 2: %d, ждали 400", code)
	}

	// Потолок можно поднять выше прежних 12.
	e.setSetting("pnl", `{"default_months": 6, "max_columns": 20}`)

	if code, r := report("?cols=2025-01,2025-02,2025-03,2025-04,2025-05,2025-06,2025-07,2025-08,2025-09,2025-10,2025-11,2025-12,2026-01"); code != http.StatusOK || r.MaxColumns != 20 {
		t.Errorf("13 колонок при потолке 20: %d, max_columns=%d", code, r.MaxColumns)
	}

	// Строки нет вовсе — значения из кода, отчёт не падает. setSetting выше
	// уже запомнил исходную строку и вернёт её целиком.
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM settings WHERE key = 'pnl'`); err != nil {
		t.Fatal(err)
	}
	if code, r := report(""); code != http.StatusOK || r.MaxColumns != 12 || len(r.Columns) != 6 {
		t.Errorf("без настройки: %d, max_columns=%d, колонок %d, ждали 12 и 6", code, r.MaxColumns, len(r.Columns))
	}
}

// Минимальная длина пароля — из settings (ключ auth): и при заведении, и при
// смене пароля.
func TestMinPasswordLengthFromSettings(t *testing.T) {
	e := setup(t)
	admin := e.user("cfo", access.CFO, 0, true)
	cookie := e.login("cfo")

	login, pw := "newbie", "fifteen chars!!!"
	create := func() int {
		return e.do(http.MethodPost, "/api/admin/users", cookie,
			oas.UserCreate{Login: &login, Password: &pw, Role: oas.Coo}).StatusCode
	}

	e.setSetting("auth", `{"session_ttl_days": 90, "login_max_failures": 5, "login_window_minutes": 15, "min_password_length": 20}`)

	if code := create(); code != http.StatusBadRequest {
		t.Errorf("пароль из 15 знаков при минимуме 20: %d, ждали 400", code)
	}
	if code := e.do(http.MethodPut, "/api/admin/users/"+itoa(admin.ID)+"/password", cookie,
		oas.PasswordSet{Password: pw}).StatusCode; code != http.StatusBadRequest {
		t.Errorf("смена на короткий пароль: %d, ждали 400", code)
	}

	e.setSetting("auth", `{"session_ttl_days": 90, "login_max_failures": 5, "login_window_minutes": 15, "min_password_length": 8}`)

	if code := create(); code != http.StatusCreated {
		t.Errorf("пароль из 15 знаков при минимуме 8: %d, ждали 201", code)
	}
}

// Ответ входа несёт пользователя: id, роль, имя, подразделение руководителя —
// фронту не нужен второй запрос. Тело то же, что у /api/me.
func TestLoginResponse(t *testing.T) {
	e := setup(t)

	if _, err := e.users.Create(context.Background(), model.UserInput{
		Login: null.StringFrom("head"), Username: null.StringFrom("Иван Петров"),
		Role: string(access.DivisionHead), DivisionID: null.IntFrom(e.sales),
	}, password); err != nil {
		t.Fatal(err)
	}
	e.user("cfo", access.CFO, 0, true)

	cookie, me := e.loginAs("head")

	if me.Id == 0 || me.Role != oas.DivisionHead || me.Username == nil || *me.Username != "Иван Петров" {
		t.Errorf("вход руководителя: %+v", me)
	}
	if me.Division == nil || me.Division.Name != "отдел продаж" || int64(me.Division.Id) != e.sales {
		t.Errorf("подразделение в ответе входа: %+v", me.Division)
	}
	if me.IsAdmin || !me.HasReport || me.LastLine == nil || *me.LastLine != "1" {
		t.Errorf("права в ответе входа: %+v", me)
	}

	// Тот же ответ, что отдаёт /api/me, — а подразделения нет, если не задано.
	if again := decode[oas.Me](t, e.do(http.MethodGet, "/api/me", cookie, nil)); again.Id != me.Id || again.Role != me.Role {
		t.Errorf("/api/me расходится с ответом входа: %+v и %+v", again, me)
	}

	_, cfo := e.loginAs("cfo")
	if cfo.Division != nil || !cfo.IsAdmin || cfo.LastLine != nil {
		t.Errorf("вход финдира: %+v", cfo)
	}
}

func TestAuthRequired(t *testing.T) {
	e := setup(t)
	e.user("cfo", access.CFO, 0, true)

	for _, path := range []string{"/api/me", "/api/pnl", "/api/snapshots", "/api/admin/users"} {
		if resp := e.do(http.MethodGet, path, "", nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s без сессии: %d", path, resp.StatusCode)
		}
		if resp := e.do(http.MethodGet, path, "forged-token", nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s с поддельной cookie: %d", path, resp.StatusCode)
		}
	}

	// Документация открыта без сессии: иначе её не из чего открыть до входа.
	for _, path := range []string{"/api/docs", "/api/openapi.yaml", "/healthz"} {
		if resp := e.do(http.MethodGet, path, "", nil); resp.StatusCode != http.StatusOK {
			t.Errorf("%s без сессии: %d, ждали 200", path, resp.StatusCode)
		}
	}

	resp := e.do(http.MethodPost, "/api/auth/login", "", oas.LoginRequest{Login: "cfo", Password: "wrong"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("неверный пароль: %d", resp.StatusCode)
	}

	cookie := e.login("cfo")

	me := decode[oas.Me](t, e.do(http.MethodGet, "/api/me", cookie, nil))
	if me.Role != oas.Cfo || !me.IsAdmin || !me.HasReport || me.LastLine != nil {
		t.Errorf("me: %+v", me)
	}

	if resp = e.do(http.MethodPost, "/api/auth/logout", cookie, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp = e.do(http.MethodGet, "/api/me", cookie, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("сессия жива после выхода: %d", resp.StatusCode)
	}
}

// Админские ручки закрыты для не-администратора — в том числе путём
// в другом регистре, который иначе прошёл бы мимо проверки префикса.
func TestAdminOnly(t *testing.T) {
	e := setup(t)
	e.user("cfo", access.CFO, 0, true)
	e.user("coo", access.COO, 0, false)

	coo := e.login("coo")

	for _, path := range []string{"/api/admin/users", "/api/admin/divisions"} {
		if resp := e.do(http.MethodGet, path, coo, nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s не-администратору: %d", path, resp.StatusCode)
		}
	}

	for _, path := range []string{"/API/admin/users", "/api/Admin/users", "/api/admin/users/"} {
		if resp := e.do(http.MethodGet, path, coo, nil); resp.StatusCode == http.StatusOK {
			t.Errorf("%s: администраторский список отдан не-администратору", path)
		}
	}

	if resp := e.do(http.MethodGet, "/api/admin/users", e.login("cfo"), nil); resp.StatusCode != http.StatusOK {
		t.Errorf("администратору: %d", resp.StatusCode)
	}
}

// Заведение, проверки, конфликт, блокировка с мгновенным отзывом сессий,
// смена пароля, защита последнего администратора.
func TestAdminUsers(t *testing.T) {
	e := setup(t)
	admin := e.user("cfo", access.CFO, 0, true)
	cookie := e.login("cfo")

	login := "Petrov "
	pw := password
	create := oas.UserCreate{Login: &login, Password: &pw, Role: oas.Coo}

	resp := e.do(http.MethodPost, "/api/admin/users", cookie, create)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, read(resp))
	}

	created := decode[oas.User](t, resp)
	if created.Login == nil || *created.Login != "petrov" || created.HasPassword == nil || !*created.HasPassword {
		t.Errorf("создан: %+v", created)
	}

	if resp = e.do(http.MethodPost, "/api/admin/users", cookie, create); resp.StatusCode != http.StatusConflict {
		t.Errorf("повтор логина: %d", resp.StatusCode)
	}

	invalid := oas.UserCreate{Role: oas.DivisionHead}
	resp = e.do(http.MethodPost, "/api/admin/users", cookie, invalid)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("неверные данные: %d", resp.StatusCode)
	}
	if body := decode[oas.Error](t, resp); body.Problems == nil || len(*body.Problems) < 2 {
		t.Errorf("проблемы не перечислены разом: %+v", body)
	}

	petrov := e.login("petrov")

	// Блокировка — следующий же запрос с его cookie получает 401.
	update := oas.UserUpdate{Login: &login, Role: oas.Coo, Blocked: true}
	if resp = e.do(http.MethodPut, "/api/admin/users/"+itoa(created.Id), cookie, update); resp.StatusCode != http.StatusOK {
		t.Fatalf("block: %d %s", resp.StatusCode, read(resp))
	}
	if resp = e.do(http.MethodGet, "/api/me", petrov, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("заблокированный читает отчёт: %d", resp.StatusCode)
	}

	// Разблокировка и смена пароля: старые сессии мертвы, новый пароль работает.
	update.Blocked = false
	e.do(http.MethodPut, "/api/admin/users/"+itoa(created.Id), cookie, update)
	petrov = e.login("petrov")

	newPw := "another long password"
	resp = e.do(http.MethodPut, "/api/admin/users/"+itoa(created.Id)+"/password", cookie, oas.PasswordSet{Password: newPw})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set password: %d %s", resp.StatusCode, read(resp))
	}
	if resp = e.do(http.MethodGet, "/api/me", petrov, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("сессия пережила смену пароля: %d", resp.StatusCode)
	}

	resp = e.do(http.MethodPost, "/api/auth/login", "", oas.LoginRequest{Login: "petrov", Password: newPw})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("новый пароль не принят: %d", resp.StatusCode)
	}

	// Последний администратор не может снять с себя права.
	self := admin.Login.String
	resp = e.do(http.MethodPut, "/api/admin/users/"+itoa(admin.ID), cookie, oas.UserUpdate{Login: &self, Role: oas.Cfo})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("последний администратор разжалован: %d", resp.StatusCode)
	}

	if resp = e.do(http.MethodPut, "/api/admin/users/999999", cookie, update); resp.StatusCode != http.StatusNotFound {
		t.Errorf("несуществующий пользователь: %d", resp.StatusCode)
	}
}

// Неверные колонки — 400 с причиной, а не 500.
func TestPnlBadColumns(t *testing.T) {
	e := setup(t)
	e.user("cfo", access.CFO, 0, true)
	cookie := e.login("cfo")

	for _, q := range []string{"cols=2026-13", "grain=quarter&cols=2026-03", "grain=week", "snapshot=abc"} {
		if resp := e.do(http.MethodGet, "/api/pnl?"+q, cookie, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s", q, resp.StatusCode, read(resp))
		}
	}

	if resp := e.do(http.MethodGet, "/api/pnl?snapshot=999999", cookie, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("несуществующий срез: %d", resp.StatusCode)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
