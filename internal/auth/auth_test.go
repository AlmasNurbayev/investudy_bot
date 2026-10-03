package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guregu/null/v6"

	"investudy_bot/internal/model"
	"investudy_bot/internal/repository"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("хеш не в PHC-формате: %s", hash)
	}

	if ok, err := VerifyPassword(hash, "correct horse"); err != nil || !ok {
		t.Errorf("верный пароль не прошёл: ok=%v err=%v", ok, err)
	}
	if ok, _ := VerifyPassword(hash, "correct horsE"); ok {
		t.Error("неверный пароль прошёл")
	}

	// Соль случайная: два хеша одного пароля разные.
	if again, _ := HashPassword("correct horse"); again == hash {
		t.Error("соль не случайная")
	}

	if _, err := VerifyPassword("$2a$10$bcrypt", "x"); err == nil {
		t.Error("чужой формат хеша не отвергнут")
	}
}

// fakeStore — память вместо базы.
type fakeStore struct {
	users    map[string]model.User
	hashes   map[string]string
	sessions map[string]repository.Session
	touched  int
	// Поля настройки auth из «settings»; менять можно по ходу теста.
	ttlDays, maxFailures, windowMinutes, minPassword int
	// settingMissing — в settings нет строки auth.
	settingMissing bool
}

func newFake(t *testing.T) *fakeStore {
	t.Helper()

	hash, err := HashPassword("secret password")
	if err != nil {
		t.Fatal(err)
	}

	return &fakeStore{
		users:    map[string]model.User{"almas": {ID: 1, Login: null.StringFrom("almas"), Role: "cfo"}},
		hashes:   map[string]string{"almas": hash},
		sessions: map[string]repository.Session{},
		ttlDays:  90, maxFailures: 5, windowMinutes: 15, minPassword: 10,
	}
}

func (f *fakeStore) AuthSettings(context.Context) (model.AuthSettings, error) {
	if f.settingMissing {
		return model.AuthSettings{}, repository.ErrSettingNotFound
	}

	return model.AuthSettings{
		SessionTTLDays:     f.ttlDays,
		LoginMaxFailures:   f.maxFailures,
		LoginWindowMinutes: f.windowMinutes,
		MinPasswordLength:  f.minPassword,
	}, nil
}

func (f *fakeStore) UserByLogin(_ context.Context, login string) (model.User, string, error) {
	u, ok := f.users[login]
	if !ok {
		return model.User{}, "", repository.ErrNotFound
	}

	return u, f.hashes[login], nil
}

func (f *fakeStore) CreateSession(_ context.Context, h []byte, userID int64, now, expires time.Time, _ string) error {
	for _, u := range f.users {
		if u.ID == userID {
			f.sessions[string(h)] = repository.Session{User: u, LastSeenAt: now, ExpiresAt: expires}
		}
	}

	return nil
}

func (f *fakeStore) SessionByToken(_ context.Context, h []byte) (repository.Session, error) {
	s, ok := f.sessions[string(h)]
	if !ok {
		return repository.Session{}, repository.ErrNotFound
	}

	return s, nil
}

func (f *fakeStore) TouchSession(_ context.Context, h []byte, now, expires time.Time) error {
	s := f.sessions[string(h)]
	s.LastSeenAt, s.ExpiresAt = now, expires
	f.sessions[string(h)] = s
	f.touched++

	return nil
}

func (f *fakeStore) DeleteSession(_ context.Context, h []byte) error {
	delete(f.sessions, string(h))
	return nil
}

var t0 = time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)

// ninetyDays — срок из сида миграции 000004.
const ninetyDays = 90 * 24 * time.Hour

func TestLoginAndAuthenticate(t *testing.T) {
	store := newFake(t)
	svc, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Логин без учёта регистра и пробелов.
	grant, err := svc.Login(ctx, " Almas ", "secret password", "1.1.1.1", "test", t0)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	token := grant.Token
	if grant.User.ID != 1 || grant.User.Role != "cfo" {
		t.Errorf("вход вернул пользователя %+v", grant.User)
	}
	if grant.TTL != ninetyDays {
		t.Errorf("срок выданной сессии %s, ждали %s", grant.TTL, ninetyDays)
	}

	for h := range store.sessions {
		if h == token {
			t.Fatal("в хранилище лёг сам токен, а не его хеш")
		}
	}

	u, cookieTTL, err := svc.Authenticate(ctx, token, t0.Add(time.Minute))
	if err != nil || u.ID != 1 || cookieTTL != 0 {
		t.Fatalf("authenticate: user=%d cookieTTL=%s err=%v", u.ID, cookieTTL, err)
	}

	// Через полчаса срок продлевается.
	if _, cookieTTL, _ = svc.Authenticate(ctx, token, t0.Add(30*time.Minute)); cookieTTL != ninetyDays || store.touched != 1 {
		t.Errorf("сессия не продлена на срок из настройки: cookieTTL=%s touched=%d", cookieTTL, store.touched)
	}

	// Скользящий срок: 90 дней от последнего продления, а не от входа.
	for _, sess := range store.sessions {
		if want := t0.Add(30*time.Minute + ninetyDays); !sess.ExpiresAt.Equal(want) {
			t.Errorf("срок %s, ждали %s", sess.ExpiresAt, want)
		}
	}
	if _, _, err = svc.Authenticate(ctx, token, t0.Add(30*time.Minute+ninetyDays)); !errors.Is(err, ErrNoSession) {
		t.Errorf("истёкшая сессия пущена: %v", err)
	}

	if err = svc.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, _, err = svc.Authenticate(ctx, token, t0); !errors.Is(err, ErrNoSession) {
		t.Errorf("сессия жива после выхода: %v", err)
	}
}

func TestLoginRejects(t *testing.T) {
	store := newFake(t)
	svc, _ := New(store)
	ctx := context.Background()

	if _, err := svc.Login(ctx, "almas", "wrong", "ip", "", t0); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("неверный пароль: %v", err)
	}

	// Несуществующий логин — та же ошибка, без подсказки, что логина нет.
	if _, err := svc.Login(ctx, "nobody", "secret password", "ip", "", t0); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("неизвестный логин: %v", err)
	}

	blocked := store.users["almas"]
	blocked.BlockedAt = null.TimeFrom(t0)
	store.users["almas"] = blocked

	if _, err := svc.Login(ctx, "almas", "secret password", "ip2", "", t0); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("заблокированный вошёл: %v", err)
	}
}

// После пяти неудач (сид настройки) с одного адреса вход заперт до конца окна
// в 15 минут, даже с верным паролем; с другого адреса — нет.
func TestLoginRateLimit(t *testing.T) {
	svc, _ := New(newFake(t))
	ctx := context.Background()

	const failureWindow = 15 * time.Minute

	for range 5 {
		_, _ = svc.Login(ctx, "almas", "wrong", "6.6.6.6", "", t0)
	}

	var tooMany TooManyAttempts
	if _, err := svc.Login(ctx, "almas", "secret password", "6.6.6.6", "", t0.Add(time.Minute)); !errors.As(err, &tooMany) {
		t.Fatalf("вход не заперт: %v", err)
	}
	if tooMany.RetryAfter != failureWindow-time.Minute {
		t.Errorf("RetryAfter = %s", tooMany.RetryAfter)
	}

	if _, err := svc.Login(ctx, "almas", "secret password", "7.7.7.7", "", t0.Add(time.Minute)); err != nil {
		t.Errorf("чужой адрес запер владельца: %v", err)
	}

	if _, err := svc.Login(ctx, "almas", "secret password", "6.6.6.6", "", t0.Add(failureWindow)); err != nil {
		t.Errorf("окно кончилось, а вход заперт: %v", err)
	}
}

// Срок берётся из настройки, а не из кода: поменяли значение — новые сессии
// и продления идут на новый срок, без перезапуска.
func TestSessionTTLFollowsSettings(t *testing.T) {
	store := newFake(t)
	svc, _ := New(store)
	ctx := context.Background()

	store.ttlDays = 7

	grant, err := svc.Login(ctx, "almas", "secret password", "ip", "", t0)
	if err != nil {
		t.Fatal(err)
	}

	week := 7 * 24 * time.Hour
	if grant.TTL != week {
		t.Errorf("TTL = %s, ждали %s", grant.TTL, week)
	}

	for _, sess := range store.sessions {
		if !sess.ExpiresAt.Equal(t0.Add(week)) {
			t.Errorf("expires_at = %s, ждали %s", sess.ExpiresAt, t0.Add(week))
		}
	}

	// Срок поменяли — продление уже по новому.
	store.ttlDays = 30

	_, cookieTTL, err := svc.Authenticate(ctx, grant.Token, t0.Add(time.Hour))
	if err != nil || cookieTTL != 30*24*time.Hour {
		t.Errorf("продление: cookieTTL=%s err=%v, ждали 720h", cookieTTL, err)
	}
}

// Нет настройки или в ней меньше дня — срок по умолчанию в 1 день, вход при
// этом не ломается. Большие значения не ограничиваются.
func TestSessionTTLFallsBackToOneDay(t *testing.T) {
	day := 24 * time.Hour

	for name, c := range map[string]struct {
		days    int
		missing bool
		want    time.Duration
	}{
		"нет строки":   {missing: true, want: day},
		"ноль":         {days: 0, want: day},
		"минус":        {days: -5, want: day},
		"один день":    {days: 1, want: day},
		"год и больше": {days: 400, want: 400 * day},
	} {
		store := newFake(t)
		svc, _ := New(store)
		store.ttlDays, store.settingMissing = c.days, c.missing

		grant, err := svc.Login(context.Background(), "almas", "secret password", "ip", "", t0)
		if err != nil {
			t.Errorf("%s: вход сломался: %v", name, err)
			continue
		}

		if grant.TTL != c.want {
			t.Errorf("%s: TTL = %s, ждали %s", name, grant.TTL, c.want)
		}
		if len(store.sessions) != 1 {
			t.Errorf("%s: сессий %d, ждали 1", name, len(store.sessions))
		}
	}
}

// Порог и окно берутся из настройки: поменяли — действует на следующей попытке.
func TestLoginRateLimitFollowsSettings(t *testing.T) {
	store := newFake(t)
	svc, _ := New(store)
	ctx := context.Background()

	store.maxFailures, store.windowMinutes = 2, 1

	for range 2 {
		_, _ = svc.Login(ctx, "almas", "wrong", "9.9.9.9", "", t0)
	}

	var tooMany TooManyAttempts
	if _, err := svc.Login(ctx, "almas", "secret password", "9.9.9.9", "", t0.Add(10*time.Second)); !errors.As(err, &tooMany) {
		t.Fatalf("после двух неудач вход не заперт: %v", err)
	}
	if tooMany.RetryAfter != 50*time.Second {
		t.Errorf("RetryAfter = %s, ждали 50s (окно 1 минута)", tooMany.RetryAfter)
	}

	if _, err := svc.Login(ctx, "almas", "secret password", "9.9.9.9", "", t0.Add(time.Minute)); err != nil {
		t.Errorf("окно в минуту кончилось, а вход заперт: %v", err)
	}
}

// Политика целиком: нет строки или пустые поля — значения из кода; заданные
// значения берутся как есть. Срок по умолчанию — сутки.
func TestLoadPolicy(t *testing.T) {
	day := 24 * time.Hour
	want := Policy{SessionTTL: day, MaxFailures: 5, FailureWindow: 15 * time.Minute, MinPasswordLen: 10}

	missing := newFake(t)
	missing.settingMissing = true

	got, err := LoadPolicy(context.Background(), missing)
	if err != nil || got != want {
		t.Errorf("нет строки: %+v, %v; ждали %+v", got, err, want)
	}

	empty := newFake(t)
	empty.ttlDays, empty.maxFailures, empty.windowMinutes, empty.minPassword = 0, -1, 0, 0

	if got, err = LoadPolicy(context.Background(), empty); err != nil || got != want {
		t.Errorf("пустые поля: %+v, %v; ждали %+v", got, err, want)
	}

	custom := newFake(t)
	custom.ttlDays, custom.maxFailures, custom.windowMinutes, custom.minPassword = 30, 3, 5, 14

	got, err = LoadPolicy(context.Background(), custom)
	wantCustom := Policy{SessionTTL: 30 * day, MaxFailures: 3, FailureWindow: 5 * time.Minute, MinPasswordLen: 14}
	if err != nil || got != wantCustom {
		t.Errorf("заданные поля: %+v, %v; ждали %+v", got, err, wantCustom)
	}
}
