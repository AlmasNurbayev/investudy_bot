package access

import (
	"errors"
	"testing"
)

func TestFor(t *testing.T) {
	cases := map[string]struct {
		user User
		want Policy
	}{
		"руководитель — до валовой и своё подразделение": {
			User{Role: DivisionHead, DivisionID: 7},
			Policy{LastLine: "1", DivisionID: 7},
		},
		"опердир — до операционной": {User{Role: COO}, Policy{LastLine: "4"}},
		"коммерческий — до чистой":  {User{Role: CCO}, Policy{LastLine: "6"}},
		"учредитель — всё":          {User{Role: Founder}, Policy{}},
		"финдир — всё":              {User{Role: CFO}, Policy{}},
		// Подразделение у не-руководителя ничего не ограничивает: база такого
		// не пустит, но если пустит — не сужать же финдиру отчёт молча.
		"подразделение у опердира игнорируется": {User{Role: COO, DivisionID: 7}, Policy{LastLine: "4"}},
		// Администратор — признак, а не роль: глубину отчёта он не меняет.
		"админ-руководитель видит неразмеченные, но не глубже": {
			User{Role: DivisionHead, DivisionID: 7, IsAdmin: true},
			Policy{LastLine: "1", DivisionID: 7, ShowUnmapped: true},
		},
	}

	for name, c := range cases {
		got, err := For(c.user)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}

		if got != c.want {
			t.Errorf("%s: %+v, ждали %+v", name, got, c.want)
		}
	}
}

func TestForRejects(t *testing.T) {
	if _, err := For(User{Role: "viewer"}); err == nil {
		t.Error("неизвестная роль получила политику")
	}

	if _, err := For(User{Role: DivisionHead}); !errors.Is(err, ErrNoDivision) {
		t.Errorf("руководитель без подразделения: %v", err)
	}
}
