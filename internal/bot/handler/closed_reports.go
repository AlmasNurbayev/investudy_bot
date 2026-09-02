// Package handler — обработчики команд бота и тексты ответов.
//
// Форматирование живёт здесь, а не в сервисе, по той же причине, по которой
// тексты для администратора лежат в cmd/parser: всё, что видит пользователь,
// собрано в одном месте, а сервис отдаёт данные.
package handler

import (
	"context"
	"errors"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"investudy_bot/internal/lib/period"
	"investudy_bot/internal/lib/snapshot"
	"investudy_bot/internal/logger"
	"investudy_bot/internal/model"
)

// Command — команда, открывающая отчёт.
const Command = "/closed_reports"

// CommandDescription — подпись команды в меню Telegram.
const CommandDescription = "Отчёты по закрытым периодам"

// CallbackPrefix отличает нажатия кнопок этого отчёта от чужих: обработчик
// callback'ов ловится по префиксу, а не по полному совпадению.
const CallbackPrefix = "closed:"

// Reports — источник отчётов (реализуется internal/report.Service).
type Reports interface {
	Closed(ctx context.Context, kind period.Kind, now time.Time) (model.ClosedReport, error)
}

type ClosedReports struct {
	reports Reports
}

func NewClosedReports(reports Reports) *ClosedReports {
	return &ClosedReports{reports: reports}
}

// Menu показывает клавиатуру выбора периода.
func (h *ClosedReports) Menu(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}

	// Кнопки в столбик: подписи длинные, в две колонки они обрезаются на
	// узких экранах.
	kinds := period.Kinds()
	rows := make([][]models.InlineKeyboardButton, 0, len(kinds))
	for _, k := range kinds {
		rows = append(rows, []models.InlineKeyboardButton{{
			Text:         k.Label(),
			CallbackData: CallbackPrefix + string(k),
		}})
	}

	send(ctx, b, update.Message.Chat.ID, "Отчёты по закрытым периодам. Выберите период:",
		&models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

// Report считает и отправляет отчёт по нажатой кнопке.
func (h *ClosedReports) Report(ctx context.Context, b *bot.Bot, update *models.Update) {
	q := update.CallbackQuery
	if q == nil {
		return
	}

	// Ответ на callback уходит первым и до всякой работы: пока он не пришёл,
	// кнопка в клиенте крутится, а отчёт считается не мгновенно.
	if _, err := b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID}); err != nil {
		logger.WRN("answer callback", "err", err)
	}

	// Telegram присылает недоступное сообщение, если кнопку нажали на старом
	// или удалённом: отвечать некуда, но падать из-за этого бот не должен.
	if q.Message.Message == nil {
		logger.WRN("callback on inaccessible message", "user", q.From.ID)
		return
	}

	chatID := q.Message.Message.Chat.ID
	kind := period.Kind(q.Data[len(CallbackPrefix):])

	rep, err := h.reports.Closed(ctx, kind, time.Now())
	if err != nil {
		logger.ERROR("closed report", "kind", kind, "user", q.From.ID, "err", err)
		send(ctx, b, chatID, failure(err), nil)

		return
	}

	for _, text := range renderClosed(rep) {
		send(ctx, b, chatID, text, nil)
	}
}

// failure переводит ошибку в текст для пользователя.
//
// «Данных ещё нет» и «отчёт не посчитался» — разные новости: в первом случае
// ждать загрузки, во втором звать администратора, и путать их нельзя.
// Остальные ошибки наружу не пересказываются: в них бывает адрес базы.
func failure(err error) string {
	if errors.Is(err, snapshot.ErrNoSnapshot) {
		return "Данные ещё не загружены. Попробуйте позже."
	}

	return "Не удалось посчитать отчёт. Администратор уже видит ошибку в логах."
}

func send(ctx context.Context, b *bot.Bot, chatID int64, text string, markup models.ReplyMarkup) {
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: markup,
	})
	if err != nil {
		// Сообщение не ушло — жаловаться больше некуда, кроме лога.
		logger.ERROR("send message", "chat", chatID, "err", err)
	}
}
