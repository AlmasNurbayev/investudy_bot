// Package bot — транспорт Telegram: запуск, проверка доступа, маршрутизация
// команд в обработчики.
package bot

import (
	"context"
	"fmt"

	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"investudy_bot/internal/bot/handler"
	"investudy_bot/internal/config"
	"investudy_bot/internal/logger"
	"investudy_bot/internal/report"
)

// Users — белый список доступа (реализуется internal/repository.Reader).
type Users interface {
	UserAllowed(ctx context.Context, telegramID int64) (bool, error)
}

type Bot struct {
	api   *tg.Bot
	users Users
	// adminID пускается всегда, минуя таблицу: сразу после миграции она пуста,
	// и без такого обхода выдать доступ первому пользователю было бы некому.
	adminID int64
}

func New(cfg config.TelegramConfig, reports *report.Service, users Users) (*Bot, error) {
	b := &Bot{users: users, adminID: cfg.AdminID}

	closed := handler.NewClosedReports(reports)

	api, err := tg.New(cfg.Token,
		tg.WithMiddlewares(b.allowed),
		tg.WithDefaultHandler(b.unknown),
		// Ошибки самой библиотеки (сеть, разбор апдейта) иначе уходят
		// в stderr мимо slog и теряются в общем логе стенда.
		tg.WithErrorsHandler(func(err error) { logger.ERROR("telegram", "err", err) }),
	)
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}

	api.RegisterHandler(tg.HandlerTypeMessageText, handler.Command, tg.MatchTypeExact, closed.Menu)
	api.RegisterHandler(tg.HandlerTypeCallbackQueryData, handler.CallbackPrefix, tg.MatchTypePrefix, closed.Report)

	b.api = api

	return b, nil
}

// Run обслуживает апдейты до отмены контекста.
//
// В отличие от парсера и prunedb бот — демон: он не отрабатывает один прогон,
// а блокируется здесь, и штатная остановка по сигналу ошибкой не является.
func (b *Bot) Run(ctx context.Context) error {
	logger.INF("bot started")
	b.api.Start(ctx)
	logger.INF("bot stopped")

	return nil
}

// allowed — проверка доступа перед любым обработчиком.
//
// Middleware, а не проверка внутри каждого хендлера: забыть её в новой команде
// проще простого, и цена забывчивости — финансовые данные постороннему.
func (b *Bot) allowed(next tg.HandlerFunc) tg.HandlerFunc {
	return func(ctx context.Context, api *tg.Bot, update *models.Update) {
		user := sender(update)
		if user == 0 {
			return
		}

		if user == b.adminID {
			next(ctx, api, update)
			return
		}

		ok, err := b.users.UserAllowed(ctx, user)
		if err != nil {
			// Ошибка базы — это не отказ в доступе, но и пустить нельзя:
			// сообщаем как о сбое, чтобы «нет доступа» не сбивало с толку.
			logger.ERROR("check access", "user", user, "err", err)
			b.reply(ctx, update, "Не удалось проверить доступ. Попробуйте позже.")

			return
		}

		if !ok {
			logger.WRN("access denied", "user", user)
			b.reply(ctx, update, "Нет доступа. Обратитесь к администратору.")

			return
		}

		next(ctx, api, update)
	}
}

func (b *Bot) unknown(ctx context.Context, _ *tg.Bot, update *models.Update) {
	b.reply(ctx, update, "Не знаю такой команды. Доступен "+handler.Command)
}

// sender достаёт автора апдейта. Апдейты без пользователя (правки каналов,
// служебные) до обработчиков не доходят вовсе.
func sender(update *models.Update) int64 {
	switch {
	case update.Message != nil:
		return update.Message.From.ID
	case update.CallbackQuery != nil:
		return update.CallbackQuery.From.ID
	}

	return 0
}

func chat(update *models.Update) int64 {
	switch {
	case update.Message != nil:
		return update.Message.Chat.ID
	case update.CallbackQuery != nil && update.CallbackQuery.Message.Message != nil:
		return update.CallbackQuery.Message.Message.Chat.ID
	}

	return 0
}

func (b *Bot) reply(ctx context.Context, update *models.Update, text string) {
	id := chat(update)
	if id == 0 {
		return
	}

	if _, err := b.api.SendMessage(ctx, &tg.SendMessageParams{ChatID: id, Text: text}); err != nil {
		logger.ERROR("send message", "chat", id, "err", err)
	}
}
