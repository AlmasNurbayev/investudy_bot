// Package access — политика доступа к ОПиУ: кому какие строки и подразделения.
//
// Одна функция на сайт и бота: вторая копия границы разошлась бы с первой
// молча, а цена расхождения — финансовые данные не тому читателю.
package access

import (
	"errors"
	"fmt"
)

// Role — роль пользователя (колонка users.role).
type Role string

const (
	DivisionHead Role = "division_head"
	COO          Role = "coo"
	CCO          Role = "cco"
	Founder      Role = "founder"
	CFO          Role = "cfo"
)

// lastLine — последняя видимая строка ОПиУ, включительно; "" — все.
//
// Константа, а не таблица: это логика, а не данные. Код строки, а не sort:
// sort правится в админке при вставке строк, а код у итога постоянный.
// Сравнение по порядку строк делает движок (pnl.Structure.Visible), и если
// кода в структуре не окажется, он вернёт ошибку, а не весь отчёт.
var lastLine = map[Role]string{
	DivisionHead: "1", // валовая прибыль
	COO:          "4", // операционная
	CCO:          "6", // чистая
	Founder:      "",
	CFO:          "",
}

// Valid — роль из списка. Таблица users роль не проверяет (в базе нет
// логики), поэтому проверка при сохранении — здесь же, рядом со списком.
func Valid(r Role) bool {
	_, ok := lastLine[r]
	return ok
}

// User — то, что политике нужно знать о пользователе.
type User struct {
	Role Role
	// DivisionID — подразделение руководителя отдела; у остальных 0.
	DivisionID int32
	IsAdmin    bool
}

// Policy — что пользователю можно видеть.
type Policy struct {
	// LastLine — код последней видимой строки ОПиУ, включительно; "" — все.
	LastLine string
	// DivisionID — единственное видимое подразделение; 0 — все. Фильтр
	// накладывается в запросе до агрегации: чужие суммы не должны попасть
	// ни в строки, ни в итоги, ни в раскрытие.
	DivisionID int32
	// ShowUnmapped — видеть суммы неразмеченных статей. Только администратору:
	// чинить разметку ему, а остальным достаточно знать, что отчёт неполон.
	ShowUnmapped bool
}

// ErrNoDivision — руководитель отдела без подразделения.
var ErrNoDivision = errors.New("руководителю отдела не назначено подразделение")

// For считает политику пользователя.
//
// Неизвестная роль и руководитель без подразделения — ошибки, а не «всё»
// и не «ничего молча»: база это уже запрещает проверками, и если такое
// всё же пришло, отчёт показывать нельзя.
func For(u User) (Policy, error) {
	last, ok := lastLine[u.Role]
	if !ok {
		return Policy{}, fmt.Errorf("неизвестная роль %q", u.Role)
	}

	p := Policy{LastLine: last, ShowUnmapped: u.IsAdmin}

	if u.Role == DivisionHead {
		if u.DivisionID == 0 {
			return Policy{}, ErrNoDivision
		}

		p.DivisionID = u.DivisionID
	}

	return p, nil
}
