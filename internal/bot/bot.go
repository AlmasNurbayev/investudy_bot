// Package bot — транспорт Telegram: запуск, проверка доступа, маршрутизация
// команд в обработчики.
package bot

import (
	"context"
	"fmt"
	"strings"

	tg "github.com/go-telegram/bot"
	tgmodels "github.com/go-telegram/bot/models"

	"investudy_bot/internal/bot/handler"
	"investudy_bot/internal/config"
	"investudy_bot/internal/logger"
	"investudy_bot/internal/report"
)

// Users — белый список доступа (реализуется internal/repository.Reader).
type Users interface {
	UserAllowed(ctx context.Context, telegramID int64) (bool, error)
}

// command — команда бота: одна запись на маршрутизацию и на меню Telegram.
//
// Таблица общая намеренно. Список команд хранится на стороне Telegram, и если
// заполнять его отдельно от регистрации хендлеров, меню разойдётся с тем, что
// бот умеет, — молча и в ту сторону, где пользователь жмёт несуществующее.
type command struct {
	name        string
	description string
	handle      tg.HandlerFunc
}

type Bot struct {
	api      *tg.Bot
	commands []command
	users    Users
	// adminID пускается всегда, минуя таблицу: сразу после миграции она пуста,
	// и без такого обхода выдать доступ первому пользователю было бы некому.
	adminID int64
}

func New(cfg config.TelegramConfig, reports *report.Service, users Users) (*Bot, error) {
	b := &Bot{users: users, adminID: cfg.AdminID}

	closed := handler.NewClosedReports(reports)

	b.commands = []command{{
		name:        handler.Command,
		description: handler.CommandDescription,
		handle:      closed.Menu,
	}}

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

	for _, c := range b.commands {
		api.RegisterHandler(tg.HandlerTypeMessageText, c.name, tg.MatchTypeExact, c.handle)
	}

	api.RegisterHandler(tg.HandlerTypeCallbackQueryData, handler.CallbackPrefix, tg.MatchTypePrefix, closed.Report)

	b.api = api

	return b, nil
}

// Run обслуживает апдейты до отмены контекста.
//
// В отличие от парсера и prunedb бот — демон: он не отрабатывает один прогон,
// а блокируется здесь, и штатная остановка по сигналу ошибкой не является.
func (b *Bot) Run(ctx context.Context) error {
	b.publishCommands(ctx)

	logger.INF("bot started")
	b.api.Start(ctx)
	logger.INF("bot stopped")

	return nil
}

// publishCommands переписывает меню команд в Telegram.
//
// Список живёт на стороне Telegram и переживает любую перевыкладку: команды,
// заведённые когда-то через BotFather, остаются в меню, даже если бот их давно
// не понимает, и вычистить их из кода иначе нельзя. SetMyCommands заменяет
// список целиком, поэтому лишние исчезают сами, а публикация при каждом старте
// делает меню производным от кода, а не отдельной настройкой, которую надо
// не забыть поправить.
//
// Неудача бота не останавливает: меню — удобство, а не условие работы.
func (b *Bot) publishCommands(ctx context.Context) {
	commands := make([]tgmodels.BotCommand, 0, len(b.commands))
	for _, c := range b.commands {
		commands = append(commands, tgmodels.BotCommand{
			// Telegram ждёт имя без ведущей косой черты.
			Command:     strings.TrimPrefix(c.name, "/"),
			Description: c.description,
		})
	}

	_, err := b.api.SetMyCommands(ctx, &tg.SetMyCommandsParams{
		Commands: commands,
		// Область по умолчанию и без языка: в неё пишет BotFather, и она же
		// видна всем, у кого нет более узкой. Узкие области бот не заводит,
		// поэтому затирать их нечем и незачем.
		Scope: &tgmodels.BotCommandScopeDefault{},
	})
	if err != nil {
		logger.ERROR("publish commands", "err", err)

		return
	}

	logger.INF("commands published", "count", len(commands))
}

// allowed — проверка доступа перед любым обработчиком.
//
// Middleware, а не проверка внутри каждого хендлера: забыть её в новой команде
// проще простого, и цена забывчивости — финансовые данные постороннему.
func (b *Bot) allowed(next tg.HandlerFunc) tg.HandlerFunc {
	return func(ctx context.Context, api *tg.Bot, update *tgmodels.Update) {
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

func (b *Bot) unknown(ctx context.Context, _ *tg.Bot, update *tgmodels.Update) {
	b.reply(ctx, update, "Не знаю такой команды. Доступен "+handler.Command)
}

// sender достаёт автора апдейта. Апдейты без пользователя (правки каналов,
// служебные) до обработчиков не доходят вовсе.
func sender(update *tgmodels.Update) int64 {
	switch {
	case update.Message != nil:
		return update.Message.From.ID
	case update.CallbackQuery != nil:
		return update.CallbackQuery.From.ID
	}

	return 0
}

func chat(update *tgmodels.Update) int64 {
	switch {
	case update.Message != nil:
		return update.Message.Chat.ID
	case update.CallbackQuery != nil && update.CallbackQuery.Message.Message != nil:
		return update.CallbackQuery.Message.Message.Chat.ID
	}

	return 0
}

func (b *Bot) reply(ctx context.Context, update *tgmodels.Update, text string) {
	id := chat(update)
	if id == 0 {
		return
	}

	if _, err := b.api.SendMessage(ctx, &tg.SendMessageParams{ChatID: id, Text: text}); err != nil {
		logger.ERROR("send message", "chat", id, "err", err)
	}
}
