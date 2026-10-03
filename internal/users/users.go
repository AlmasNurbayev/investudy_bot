// Package users — заведение и правка пользователей администратором.
//
// Здесь живут проверки, которых нет в базе: в миграциях только ключи, UNIQUE
// и NOT NULL (CLAUDE.md, требования к миграциям). Допустимая роль,
// подразделение руководителя, наличие способа входа, защита от потери
// последнего администратора — всё это код, и обойти его можно только SQL
// руками.
package users

import (
	"context"
	"fmt"
	"strings"

	"github.com/guregu/null/v6"

	"investudy_bot/internal/access"
	"investudy_bot/internal/auth"
	"investudy_bot/internal/model"
	"investudy_bot/internal/repository"
)

// ValidationError — правка отвергнута; Problems — по пункту на нарушение,
// чтобы форма показала всё сразу, а не по одному за отправку.
type ValidationError struct {
	Problems []string
}

func (e ValidationError) Error() string {
	return "неверные данные пользователя: " + strings.Join(e.Problems, "; ")
}

// Store — хранилище пользователей (реализует repository.Users).
type Store interface {
	AuthSettings(ctx context.Context) (model.AuthSettings, error)
	ListUsers(ctx context.Context) ([]model.User, error)
	UserByID(ctx context.Context, id int64) (model.User, error)
	CreateUser(ctx context.Context, in model.UserInput, hash string) (int64, error)
	UpdateUser(ctx context.Context, id int64, in model.UserInput, check repository.UpdateCheck) error
	SetPassword(ctx context.Context, id int64, hash string) error
}

type Service struct {
	store Store
}

func New(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) List(ctx context.Context) ([]model.User, error) {
	return s.store.ListUsers(ctx)
}

// Create заводит пользователя. password обязателен, если задан логин:
// логин без пароля войти не даёт, а заводят его как раз для входа.
func (s *Service) Create(ctx context.Context, in model.UserInput, password string) (model.User, error) {
	in = normalize(in)

	problems := Validate(in)
	if in.Login.Valid {
		policy, err := auth.LoadPolicy(ctx, s.store)
		if err != nil {
			return model.User{}, err
		}

		problems = append(problems, checkPassword(password, policy.MinPasswordLen)...)
	} else if password != "" {
		problems = append(problems, "пароль без логина не нужен: войти на сайт без логина нельзя")
	}

	if len(problems) > 0 {
		return model.User{}, ValidationError{Problems: problems}
	}

	var hash string
	if password != "" {
		var err error
		if hash, err = auth.HashPassword(password); err != nil {
			return model.User{}, err
		}
	}

	id, err := s.store.CreateUser(ctx, in, hash)
	if err != nil {
		return model.User{}, err
	}

	return s.store.UserByID(ctx, id)
}

// Update переписывает пользователя целиком от имени actor.
func (s *Service) Update(ctx context.Context, actor model.User, id int64, in model.UserInput) (model.User, error) {
	in = normalize(in)

	if problems := Validate(in); len(problems) > 0 {
		return model.User{}, ValidationError{Problems: problems}
	}

	err := s.store.UpdateUser(ctx, id, in, func(before model.User, admins []int64) error {
		if problems := Guard(actor.ID, before, in, admins); len(problems) > 0 {
			return ValidationError{Problems: problems}
		}

		return nil
	})
	if err != nil {
		return model.User{}, err
	}

	return s.store.UserByID(ctx, id)
}

// SetPassword задаёт пароль. Старый не спрашивается: менять пароли может
// только администратор, и хеш руками не посчитать.
func (s *Service) SetPassword(ctx context.Context, id int64, password string) error {
	policy, err := auth.LoadPolicy(ctx, s.store)
	if err != nil {
		return err
	}

	if problems := checkPassword(password, policy.MinPasswordLen); len(problems) > 0 {
		return ValidationError{Problems: problems}
	}

	user, err := s.store.UserByID(ctx, id)
	if err != nil {
		return err
	}

	if !user.Login.Valid {
		return ValidationError{Problems: []string{"сначала задайте логин: без него пароль не нужен"}}
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	return s.store.SetPassword(ctx, id, hash)
}

// Validate проверяет поля пользователя сами по себе.
func Validate(in model.UserInput) []string {
	var problems []string

	role := access.Role(in.Role)
	if !access.Valid(role) {
		problems = append(problems, fmt.Sprintf("неизвестная роль %q", in.Role))
	}

	switch {
	case role == access.DivisionHead && !in.DivisionID.Valid:
		problems = append(problems, "руководителю отдела нужно подразделение")
	case role != access.DivisionHead && in.DivisionID.Valid:
		problems = append(problems, "подразделение задаётся только руководителю отдела")
	}

	if !in.Login.Valid && !in.TelegramID.Valid {
		problems = append(problems, "нужен логин или Telegram-id: иначе войти некуда")
	}

	if in.TelegramID.Valid && in.TelegramID.Int64 <= 0 {
		problems = append(problems, "Telegram-id — положительное число")
	}

	return problems
}

// Guard проверяет правку пользователя before на то, чтобы администраторы
// не остались без админки. activeAdmins — id незаблокированных администраторов.
func Guard(actorID int64, before model.User, in model.UserInput, activeAdmins []int64) []string {
	var problems []string

	if before.ID == actorID {
		if in.Blocked {
			problems = append(problems, "нельзя заблокировать себя")
		}
		if !in.IsAdmin {
			problems = append(problems, "нельзя снять права администратора с себя")
		}
	}

	wasActiveAdmin := before.IsAdmin && !before.BlockedAt.Valid
	staysActiveAdmin := in.IsAdmin && !in.Blocked

	if wasActiveAdmin && !staysActiveAdmin && len(activeAdmins) <= 1 {
		problems = append(problems, "это последний администратор: сначала назначьте другого")
	}

	return problems
}

func checkPassword(password string, minLen int) []string {
	if len([]rune(password)) < minLen {
		return []string{fmt.Sprintf("пароль короче %d символов", minLen)}
	}

	return nil
}

// normalize приводит поля к виду хранения: логин — строчными без пробелов,
// пустые строки — NULL. Иначе «Almas» и «almas» стали бы двумя логинами,
// а пустой логин занял бы UNIQUE.
func normalize(in model.UserInput) model.UserInput {
	if in.Login.Valid {
		l := auth.NormalizeLogin(in.Login.String)
		in.Login = null.NewString(l, l != "")
	}

	if in.Username.Valid {
		u := strings.TrimSpace(in.Username.String)
		in.Username = null.NewString(u, u != "")
	}

	return in
}
