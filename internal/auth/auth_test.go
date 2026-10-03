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
	}
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

func TestLoginAndAuthenticate(t *testing.T) {
	store := newFake(t)
	svc, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Логин без учёта регистра и пробелов.
	token, err := svc.Login(ctx, " Almas ", "secret password", "1.1.1.1", "test", t0)
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	for h := range store.sessions {
		if h == token {
			t.Fatal("в хранилище лёг сам токен, а не его хеш")
		}
	}

	u, renewed, err := svc.Authenticate(ctx, token, t0.Add(time.Minute))
	if err != nil || u.ID != 1 || renewed {
		t.Fatalf("authenticate: user=%d renewed=%v err=%v", u.ID, renewed, err)
	}

	// Через полчаса срок продлевается.
	if _, renewed, _ = svc.Authenticate(ctx, token, t0.Add(30*time.Minute)); !renewed || store.touched != 1 {
		t.Errorf("сессия не продлена: renewed=%v touched=%d", renewed, store.touched)
	}

	// Скользящий срок: 90 дней от последнего продления, а не от входа.
	for _, sess := range store.sessions {
		if want := t0.Add(30*time.Minute + SessionTTL); !sess.ExpiresAt.Equal(want) {
			t.Errorf("срок %s, ждали %s", sess.ExpiresAt, want)
		}
	}
	if _, _, err = svc.Authenticate(ctx, token, t0.Add(30*time.Minute+SessionTTL)); !errors.Is(err, ErrNoSession) {
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

// После maxFailures неудач с одного адреса вход заперт до конца окна, даже
// с верным паролем; с другого адреса — нет.
func TestLoginRateLimit(t *testing.T) {
	svc, _ := New(newFake(t))
	ctx := context.Background()

	for range maxFailures {
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
