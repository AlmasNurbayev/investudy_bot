package users

import (
	"strings"
	"testing"
	"time"

	"github.com/guregu/null/v6"

	"investudy_bot/internal/model"
)

func TestValidate(t *testing.T) {
	ok := []model.UserInput{
		{Login: null.StringFrom("a"), Role: "cfo"},
		{TelegramID: null.IntFrom(42), Role: "coo"},
		{Login: null.StringFrom("a"), Role: "division_head", DivisionID: null.IntFrom(3)},
	}
	for _, in := range ok {
		if p := Validate(in); len(p) > 0 {
			t.Errorf("%+v: %v", in, p)
		}
	}

	bad := map[string]model.UserInput{
		"роль viewer": {Login: null.StringFrom("a"), Role: "viewer"},
		"руководитель без подразделения": {Login: null.StringFrom("a"), Role: "division_head"},
		"подразделение у опердира":       {Login: null.StringFrom("a"), Role: "coo", DivisionID: null.IntFrom(3)},
		"ни логина, ни Telegram":         {Role: "cfo"},
		"отрицательный Telegram-id":      {TelegramID: null.IntFrom(-1), Role: "cfo"},
	}
	for name, in := range bad {
		if p := Validate(in); len(p) == 0 {
			t.Errorf("%s: прошло", name)
		}
	}
}

// Пустой логин — NULL, а не ” в UNIQUE; регистр и пробелы — один логин.
func TestNormalize(t *testing.T) {
	in := normalize(model.UserInput{Login: null.StringFrom("  Almas "), Username: null.StringFrom("   ")})
	if in.Login.String != "almas" || in.Username.Valid {
		t.Errorf("normalize: %+v", in)
	}

	if in = normalize(model.UserInput{Login: null.StringFrom(" ")}); in.Login.Valid {
		t.Error("пустой логин не стал NULL")
	}
}

func TestGuard(t *testing.T) {
	admin := model.User{ID: 1, IsAdmin: true}
	other := model.User{ID: 2, IsAdmin: true}
	keep := model.UserInput{IsAdmin: true}

	cases := []struct {
		name   string
		before model.User
		in     model.UserInput
		admins []int64
		want   string // подстрока проблемы; "" — пропустить
	}{
		{"себя заблокировать", admin, model.UserInput{IsAdmin: true, Blocked: true}, []int64{1, 2}, "себя"},
		{"с себя снять админа", admin, model.UserInput{}, []int64{1, 2}, "с себя"},
		{"последнего админа заблокировать", other, model.UserInput{IsAdmin: true, Blocked: true}, []int64{2}, "последний"},
		{"последнего админа разжаловать", other, model.UserInput{}, []int64{2}, "последний"},
		{"второго админа разжаловать", other, model.UserInput{}, []int64{1, 2}, ""},
		{"себя оставить админом", admin, keep, []int64{1}, ""},
		{"заблокированного админа разжаловать", model.User{ID: 3, IsAdmin: true, BlockedAt: null.TimeFrom(t0())}, model.UserInput{}, []int64{1}, ""},
	}

	for _, c := range cases {
		got := strings.Join(Guard(1, c.before, c.in, c.admins), "; ")

		switch {
		case c.want == "" && got != "":
			t.Errorf("%s: отвергнуто: %s", c.name, got)
		case c.want != "" && !strings.Contains(got, c.want):
			t.Errorf("%s: %q, ждали %q", c.name, got, c.want)
		}
	}
}

func t0() time.Time { return time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC) }
