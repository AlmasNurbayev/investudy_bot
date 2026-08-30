package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"investudy_bot/internal/config"
	"investudy_bot/internal/db"
	"investudy_bot/internal/logger"
	"investudy_bot/internal/notify"
	"investudy_bot/internal/parser"
	"investudy_bot/internal/repository"
	"investudy_bot/internal/sheets"
)

// notifyTimeout — сколько ждём Telegram. Оповещение отправляется уже после
// провала, поэтому висеть на нём дольше нескольких секунд незачем.
const notifyTimeout = 15 * time.Second

func main() {
	logger.Init(slog.LevelDebug)

	cfg, err := config.Load()
	if err != nil {
		// Единственная ошибка, о которой некому сообщить: и токен, и адресат
		// лежат в том же конфиге, который не прочитался.
		logger.ERROR("config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tg := notify.NewTelegram(cfg.Telegram)

	// Один синк и выход: расписание — на кроне. Ненулевой код возврата
	// нужен, чтобы крон увидел неудачу.
	report, err := run(ctx, cfg)
	if err != nil {
		logger.ERROR("parser failed", "err", err)
		notifyAdmin(tg, failureMessage(err))
		os.Exit(1)
	}

	// Об удачном прогоне сообщаем тоже: молчание неотличимо от «крон не
	// запустился», а из отчёта видно, сколько строк доехало и сколько из них
	// без аналитики.
	notifyAdmin(tg, successMessage(report))
}

// run держит весь прогон в одной функции, чтобы defer'ы отработали: os.Exit
// в main их не выполняет, и соединение с базой закрылось бы только смертью
// процесса.
func run(ctx context.Context, cfg config.Config) (parser.Report, error) {
	conn, err := db.New(ctx, cfg.Postgres)
	if err != nil {
		return parser.Report{}, fmt.Errorf("database: %w", err)
	}
	// Закрытие идёт по своему контексту: ctx к этому моменту уже отменён сигналом.
	defer conn.Close(context.Background())

	client, err := sheets.New(ctx, cfg.Sheets)
	if err != nil {
		return parser.Report{}, fmt.Errorf("sheets: %w", err)
	}

	logger.INF("parser started")

	svc := parser.New(client, store{repository.NewStore(conn)})

	report, err := svc.Sync(ctx)
	if err != nil {
		return parser.Report{}, fmt.Errorf("sync: %w", err)
	}

	return report, nil
}

func notifyAdmin(tg *notify.Telegram, text string) {
	// Контекст свой: ctx прогона к этому моменту мог быть уже отменён сигналом,
	// а сообщение нужно отправить именно тогда, когда всё пошло не так.
	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()

	if err := tg.Send(ctx, text); err != nil {
		// Неудача оповещения не меняет исхода прогона: причина уже в логе,
		// а код возврата выставлен по самому прогону, а не по отправке.
		logger.ERROR("notify admin", "err", err)
	}
}

// successMessage — отчёт об удачном прогоне.
//
// Строки без обязательной аналитики вынесены отдельным блоком: загрузились они
// нормально, но в разрезах отчёта их не видно, и заметить это можно только
// здесь.
func successMessage(report parser.Report) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Загрузка выполнена.\n\nСрез №%d\nЗагружено строк: %d\nВремя: %s\n\n",
		report.SnapshotID, report.Rows, report.Took.Round(time.Millisecond))

	g := report.Gaps
	if g.Rows == 0 {
		b.WriteString("Обязательная аналитика заполнена во всех строках.")

		return b.String()
	}

	fmt.Fprintf(&b, "Строк без обязательной аналитики: %d\n", g.Rows)
	fmt.Fprintf(&b, "— без подразделения: %d\n— без статьи: %d\n— без периода: %d",
		g.NoDivision, g.NoItem, g.NoPeriod)

	return b.String()
}

// failureMessage превращает ошибку прогона в текст для администратора.
//
// Разбор строки листа расписывается по полям: чинить данные адресат пойдёт
// в Google Sheets, и ему нужно знать, куда смотреть — строка, банк,
// организация, адрес ячейки и то, что в ней лежит сейчас.
func failureMessage(cause error) string {
	var b strings.Builder

	b.WriteString("Загрузка данных не удалась.\n\n")

	var re *sheets.RowError
	if !errors.As(cause, &re) {
		b.WriteString(cause.Error())

		return b.String()
	}

	fmt.Fprintf(&b, "Лист: %s\nСтрока: %d\nБанк: %s\nОрганизация: %s\n\n",
		re.Sheet, re.Row, orDash(re.Bank), orDash(re.Organization))

	fmt.Fprintf(&b, "Ячейка %s, колонка «%s»:\nожидалось %s, а лежит «%s».",
		re.Cell, re.Column, re.Want, re.Value)

	return b.String()
}

// orDash подставляет прочерк вместо пустого поля: пустое место в сообщении
// читается как потеря при форматировании, а прочерк — как пустая ячейка.
func orDash(s string) string {
	if s == "" {
		return "—"
	}

	return s
}

// store подгоняет конкретный *repository.Tx под интерфейс parser.Tx: Go не считает
// метод, возвращающий конкретный тип, реализацией метода, возвращающего интерфейс.
type store struct {
	*repository.Store
}

func (s store) Begin(ctx context.Context) (parser.Tx, error) {
	return s.Store.Begin(ctx)
}
