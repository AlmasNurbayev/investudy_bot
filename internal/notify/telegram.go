// Package notify доставляет администратору сообщения о работе сервисов.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"investudy_bot/internal/config"
)

const apiBase = "https://api.telegram.org"

// maxMessage — лимит Bot API на длину текста. Берём с запасом: лимит считается
// в кодовых единицах UTF-16, а мы режем по рунам, и у эмодзи с кириллицей
// эти счётчики расходятся.
const maxMessage = 3800

// Telegram отправляет сообщения одному адресату — администратору.
//
// Библиотека go-telegram/bot сюда не берётся по той же причине, по которой не
// берётся SDK Google Sheets: нужен ровно один POST sendMessage, а библиотека
// тянет опрос апдейтов и маршрутизацию хендлеров, которые парсеру не нужны.
// Боту она понадобится — там она и появится.
type Telegram struct {
	http   *http.Client
	base   string // адрес API; подменяется тестом
	token  string
	chatID int64
}

func NewTelegram(cfg config.TelegramConfig) *Telegram {
	// Таймаут задаёт вызывающий через контекст: у оповещения о падении он свой,
	// короткий, и не совпадает с таймаутами самой работы.
	return &Telegram{
		http:   &http.Client{},
		base:   apiBase,
		token:  cfg.Token,
		chatID: cfg.AdminID,
	}
}

// Send шлёт текст администратору.
func (t *Telegram) Send(ctx context.Context, text string) error {
	// parse_mode не задаётся намеренно: в тексте едет содержимое ячеек листа
	// как есть, и любое подчёркивание в назначении платежа или угловая скобка
	// в имени контрагента сломали бы разбор Markdown/HTML — Bot API ответил бы
	// ошибкой ровно тогда, когда сообщение важнее всего. Плоский текст
	// экранировать не нужно вовсе.
	form := url.Values{
		"chat_id": {strconv.FormatInt(t.chatID, 10)},
		"text":    {truncate(text, maxMessage)},
	}

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", t.base, t.token)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.http.Do(req)
	if err != nil {
		// Токен лежит в пути URL, а ошибки транспорта тащат URL целиком —
		// в лог должен попасть только повод, без секрета.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}

		return fmt.Errorf("sendMessage: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	_ = json.Unmarshal(body, &result)

	// Bot API отвечает 200 только на удачу, но description информативнее кода:
	// «chat not found» и «bot was blocked» приходят одинаковым 400.
	if !result.OK {
		if result.Description == "" {
			result.Description = strings.TrimSpace(string(body))
		}

		return fmt.Errorf("sendMessage: %s: %s", resp.Status, result.Description)
	}

	return nil
}

// truncate обрезает текст по рунам: обрезка по байтам разрубила бы кириллицу
// посередине символа, и Bot API отверг бы сообщение как невалидный UTF-8.
func truncate(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}

	return string(r[:limit]) + "…"
}
