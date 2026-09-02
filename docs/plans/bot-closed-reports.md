# cmd/bot: команда /closed_reports

## Context

Четвёртый бинарник из таблицы в `CLAUDE.md` — `cmd/bot` — до сих пор не написан. Цепочка
Sheets → PostgreSQL работает (`cmd/parser`, `cmd/prunedb`, `cmd/migrator`), но конечный
потребитель данных отсутствует: посмотреть цифры можно только руками через psql
(`docs/queries.md`).

Нужен первый рабочий сценарий: команда `/closed_reports` («отчёты по закрытым периодам»)
с кнопками «текущий месяц», «прошлый месяц», «текущий квартал», «прошлый квартал»,
отдающая сводную таблицу по разрезам `division → item → sub_item` с суммами `debet`/`credit`.
Из сводки исключаются технические статьи (`пополнение`, `перевод`, `пополнение на оператора`,
`движение денег`) — они не расходы и не доходы, а внутреннее движение, и без их отсечения
итоги задваиваются.

Слоями: **handler → service → repository**. Хелпер границ дат — в `internal/lib/period`.
Список исключаемых статей — в таблице настроек в БД (`value JSON`), чтобы его можно было
править без пересборки, в будущем — визуальным редактором.

Читающий слой в проекте ещё не существует вовсе: `internal/repository` умеет только писать,
`internal/db` отдаёт один `*pgx.Conn` без методов `Query`. Бот — демон, обслуживающий
апдейты конкурентно, поэтому read-путь и пул соединений создаются в рамках этой же работы.

### Решения, принятые с пользователем

| Вопрос | Решение |
| --- | --- |
| Формат вывода | Плоская иерархическая таблица одним махом, `<pre>`, при переполнении — несколько сообщений подряд |
| Колонка периода | `data.period` (учётный период, всегда `01.MM.YYYY`) |
| Доступ | Таблица `users` в БД; `TELEGRAM_ADMIN_ID` из env пускается всегда (bootstrap) |
| Тип настроек | `settings(key TEXT PK, value JSON, …)` |
| Версия среза | Новейший **непустой**; правило выбора — хелпером в `internal/lib/snapshot` |

---

## 1. Миграция `migrate/000002_bot_schema.{up,down}.sql`

```sql
-- up
CREATE TABLE IF NOT EXISTS users (
    telegram_id BIGINT PRIMARY KEY,
    username    TEXT,
    role        TEXT NOT NULL DEFAULT 'viewer',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS settings (
    key         TEXT PRIMARY KEY,
    value       JSON NOT NULL,
    description TEXT,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO settings (key, value, description) VALUES (
    'closed_reports',
    '{"excluded_items": ["пополнение", "перевод", "пополнение на оператора", "движение денег"]}',
    'Отчёт /closed_reports: статьи, исключаемые из сводки'
) ON CONFLICT (key) DO NOTHING;
```

`down` — `DROP TABLE IF EXISTS settings; DROP TABLE IF EXISTS users;`.

**Отступление от правила «только DDL» в `CLAUDE.md` — намеренное и узкое.** Правило запрещает
`TRUNCATE`/`UPDATE`/`DELETE`, то есть порчу данных; `INSERT … ON CONFLICT DO NOTHING` ничего не
портит и никогда не затирает отредактированное значение. Альтернатива — дефолт константой в Go —
даёт два источника правды и делает пустую таблицу настроек невидимой проблемой. В комментарии
к миграции это надо зафиксировать, и заодно дописать оговорку в `CLAUDE.md`.

**`json`, а не `jsonb`** — сохраняет текст ровно как записан (отступы, порядок ключей), чтобы
round-trip через будущий визуальный редактор не переформатировал файл. Операторы и индексы
для десятка настроек не нужны.

Отсутствие строки `closed_reports` — **громкая ошибка**, а не пустой список исключений: молча
посчитать сводку с «пополнениями» внутри хуже, чем не ответить (та же логика, что у
`parser.Sync` с пустым листом — `internal/parser/parser.go:84`).

**Индексов не добавляем**: отчётный запрос фильтрует по `(snapshot_id, period)`, это уже
`data_snapshot_period_idx`; справочники — десятки строк.

## 2. `internal/lib/period` — границы дат

Новый пакет `internal/lib/period/period.go`:

```go
type Kind string

const (
    CurrentMonth    Kind = "cur_month"
    PreviousMonth   Kind = "prev_month"
    CurrentQuarter  Kind = "cur_quarter"
    PreviousQuarter Kind = "prev_quarter"
)

// Range — полуинтервал [From, To): To не входит.
type Range struct {
    From, To time.Time
    Title    string // «Сентябрь 2026», «III квартал 2026»
}

func Resolve(k Kind, now time.Time) (Range, error)
func Kinds() []Kind          // порядок кнопок в клавиатуре
func (k Kind) Label() string // «Текущий месяц» и т.д.
```

- Полуинтервал, а не `BETWEEN`, — снимает вопрос про «последний день 23:59:59»; `period` —
  `DATE`, так что `period >= $1 AND period < $2` попадает ровно в месяц/квартал.
- Арифметика только через `time.Date(y, m-1, 1, 0,0,0,0, loc)` — нормализация месяца 0 → декабрь
  прошлого года делает переход через год бесплатным. `AddDate` по произвольному дню (31 марта − 1
  месяц = 3 марта) не использовать.
- **Зона в Go не зашивается.** `now` приходит параметром, вызывающий передаёт `time.Now()`, а зона
  процесса задана `TZ=Asia/Almaty` в `docker-compose.yml` — там же, где она задана контейнеру
  Postgres. Третьего места, которое надо синхронизировать (`CLAUDE.md` предупреждает ровно об
  этом), не появляется.
- Русские названия месяцев — своим массивом, в `time` их нет.

Пакет чистый (без БД и сети) — покрывается обычным unit-тестом в стиле
`internal/config/config_test.go`.

## 3. `internal/lib/snapshot` — выбор рабочей версии среза

По умолчанию отчёт считается по **новейшему непустому** срезу: если последний прогон парсера
записал версию без строк, отчёт должен показать предыдущую, а не пустую таблицу.

```go
var ErrNoSnapshot = errors.New("нет ни одного непустого среза")

// Usable — срез пригоден для отчёта: заливка завершена и строки есть.
func Usable(s model.Snapshot) bool { return s.RowCount.Valid && s.RowCount.Int64 > 0 }

// Latest выбирает рабочую версию из списка, отсортированного по taken_at DESC.
func Latest(snapshots []model.Snapshot) (model.Snapshot, error)
```

Пакет чистый: список срезов приносит репозиторий (`ListSnapshots`), решение принимает хелпер.
Так правило «какой срез считается рабочим» лежит в одном месте и покрывается unit-тестом без БД,
а будущий веб-отчёт возьмёт тот же `snapshot.Latest`, а не заведёт своё определение.

`RowCount` пишется `FinishSnapshot` в той же транзакции, что и заливка
(`internal/repository/repository.go:64`), поэтому у любого закоммиченного среза оно проставлено;
`Valid` проверяется на случай ручной правки в БД. Сверять `row_count` с реальным
`count(*)` по `data` не нужно: `prunedb` удаляет срез вместе со строками через
`ON DELETE CASCADE`, полупустых версий не бывает.

### Следствие: бот перестаёт читать `data_current`

`data_current` (`migrate/000001_init_schema.up.sql`) прибит к новейшему срезу по `taken_at`
безусловно — «новейший непустой» через него не выразить. Поэтому бот читает `data` с **явным**
`snapshot_id`, полученным от `snapshot.Latest`.

Правило `docs/data-versioning.md:98` («бот читает только `data_current`») существовало ради
того, чтобы нельзя было забыть фильтр по версии. Оно остаётся выполненным по сути: `snapshot_id`
— обязательный параметр `Reader.ClosedReport`, забыть его невозможно, версия разрешается в
единственном месте. Формулировку в `docs/data-versioning.md` надо обновить: не «только
`data_current`», а «только через разрешённый `snapshot_id`».

## 4. Слой данных

### `internal/db/pool.go` — пул

`db.Conn` — один `*pgx.Conn`, он не безопасен для конкурентного использования и боту не годится.
Добавить рядом, не трогая существующий тип (парсер и prunedb остаются на `Conn`):

```go
type Pool struct { pool *pgxpool.Pool; cfg config.PostgresConfig }

func NewPool(ctx context.Context, cfg config.PostgresConfig) (*Pool, error)
func (p *Pool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
func (p *Pool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
func (p *Pool) Begin(ctx context.Context) (pgx.Tx, error) // заодно удовлетворяет repository.Beginner
func (p *Pool) Close()
```

`pgxpool` уже доступен: `github.com/jackc/puddle/v2` есть в `go.mod` как indirect от pgx, новый
модуль не нужен. `MaxConns` — 4: бот делает редкие тяжёлые агрегаты, не тысячи RPS. Ping при
старте — как в `db.New` (`internal/db/db.go:28`).

### `internal/repository/reader.go` — чтение

```go
// Querier — источник запросов (его реализует internal/db.Pool).
type Querier interface {
    Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
    QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Reader struct { db Querier }
func NewReader(db Querier) *Reader

// ListSnapshots — свежие версии, taken_at DESC; из них snapshot.Latest берёт рабочую.
func (r *Reader) ListSnapshots(ctx context.Context, limit int) ([]model.Snapshot, error)
func (r *Reader) ClosedReport(ctx context.Context, snapshotID int64, from, to time.Time, excluded []string) ([]model.ReportRow, error)
```

`Store` (запись) не трогаем — у него свой интерфейс `Beginner`. `ListSnapshots(ctx, limit)` —
ровно та сигнатура, что заложена контрактом `Reader` в `docs/data-versioning.md:193`;
`Report(snapshotID, filter)` в общем виде откладываем до веб-отчёта, боту нужен один разрез.

Запрос идёт по `data` с явным `snapshot_id` (см. раздел 3):

```sql
SELECT coalesce(dv.name, '—'), coalesce(it.name, '—'), coalesce(si.name, '—'),
       coalesce(sum(d.debet), 0), coalesce(sum(d.credit), 0)
FROM data d
LEFT JOIN divisions dv ON dv.id = d.division_id
LEFT JOIN items     it ON it.id = d.item_id
LEFT JOIN sub_items si ON si.id = d.sub_item_id
WHERE d.snapshot_id = $1
  AND d.period >= $2 AND d.period < $3
  AND (it.name IS NULL OR lower(it.name) <> ALL ($4::text[]))
GROUP BY 1, 2, 3
ORDER BY 1, 2, 3
```

- `snapshot_id` первым в `WHERE` — под это и построен `data_snapshot_period_idx`
  (`snapshot_id, period`), индекс работает целиком.
- `LEFT JOIN` — все FK в `data` nullable; строки без аналитики не должны исчезать из сводки
  молча (парсер их специально считает как `Gaps`).
- Сравнение исключений по `lower()`: в листе статья может быть записана «Пополнение», а в
  настройках лежит «пополнение». Список приводится к нижнему регистру в сервисе перед передачей.
- `it.name IS NULL OR` обязателен: `NULL <> ALL (…)` даёт `NULL`, и строки без статьи выпали бы.
- Суммы сканируются в `pgtype.Numeric`, а не в `null.Float`: это деньги, `NUMERIC` в Postgres
  точен (`docs/data-versioning.md:248`), и терять эту точность на выводе незачем. Форматирование —
  маленький хелпер `internal/lib/money.Format`.

### `internal/repository/settings.go`, `internal/repository/users.go`

```go
func (r *Reader) ClosedReportsSettings(ctx context.Context) (model.ClosedReportsSettings, error)
func (r *Reader) UserAllowed(ctx context.Context, telegramID int64) (bool, error)
```

Первый читает `value` из `settings WHERE key = 'closed_reports'` и разбирает `encoding/json`
в структуру; `pgx.ErrNoRows` заворачивается в понятную ошибку «настройка не найдена, накатите
миграции». Второй — `SELECT EXISTS(SELECT 1 FROM users WHERE telegram_id = $1)`.

### `internal/model` — дополнения

```go
type ReportRow struct { Division, Item, SubItem string; Debet, Credit pgtype.Numeric }
type ClosedReportsSettings struct { ExcludedItems []string `json:"excluded_items"` }
type ClosedReport struct {
    Title      string
    Snapshot   Snapshot // фактически использованная версия
    Stale      bool     // использован не новейший срез: новейший оказался пустым
    Rows       []ReportRow
    TotalDebet, TotalCredit pgtype.Numeric
}
```

## 5. `internal/report` — сервис

```go
type Reader interface {
    ListSnapshots(ctx context.Context, limit int) ([]model.Snapshot, error)
    ClosedReport(ctx context.Context, snapshotID int64, from, to time.Time, excluded []string) ([]model.ReportRow, error)
    ClosedReportsSettings(ctx context.Context) (model.ClosedReportsSettings, error)
}

type Service struct { reader Reader }
func New(reader Reader) *Service
func (s *Service) Closed(ctx context.Context, kind period.Kind, now time.Time) (model.ClosedReport, error)
```

Интерфейс описан **в пакете-потребителе** — конвенция из `CLAUDE.md`, ровно как
`parser.Fetcher`/`parser.Store` (`internal/parser/parser.go:13`). Порядок в `Closed`:
`period.Resolve` → `ListSnapshots(ctx, snapshotCandidates)` → `snapshot.Latest` → настройки →
`lower()` списка исключений → запрос по выбранному `snapshot_id` → итоги. Форматирования текста
тут нет — сервис отдаёт данные, как `parser.Sync` отдаёт `Report`.

`snapshotCandidates` — константа пакета (10): столько версий заведомо хватает, чтобы найти
непустую, а полный список тянуть незачем. Если непустых нет — наружу уходит
`snapshot.ErrNoSnapshot`, хендлер превращает его в «данные ещё не загружены».

В `model.ClosedReport` кладётся выбранный `Snapshot` и признак `Stale` — выбранный срез не
новейший. Пользователь должен видеть, что показаны не самые свежие данные: молчаливый откат на
предыдущую версию выглядит как «отчёт почему-то вчерашний» — ровно та проблема, ради которой
парсер пишет администратору (`CLAUDE.md`, «Ключевые архитектурные решения»).

## 6. `internal/bot` — транспорт

`internal/bot/bot.go`:

```go
type Users interface { UserAllowed(ctx context.Context, telegramID int64) (bool, error) }

type Bot struct { api *bot.Bot; report *report.Service; users Users; adminID int64 }
func New(cfg config.TelegramConfig, rep *report.Service, users Users) (*Bot, error)
func (b *Bot) Run(ctx context.Context) error // блокируется на api.Start(ctx)
```

- `bot.New(token, bot.WithMiddlewares(b.access, b.logging), bot.WithDefaultHandler(b.unknown))`.
- Регистрация: `RegisterHandler(bot.HandlerTypeMessageText, "/closed_reports", bot.MatchTypeExact, …)`
  и `RegisterHandler(bot.HandlerTypeCallbackQueryData, "closed:", bot.MatchTypePrefix, …)`.
  Точные имена сверить с версией пакета при реализации.
- `internal/bot/middleware.go` — доступ: `adminID` из env пускается всегда (bootstrap — иначе
  после миграции в боте не будет ни одного пользователя), остальные проверяются по `users`.
  Отказ — короткий ответ «нет доступа» и запись в лог.

`internal/bot/handler/closed_reports.go`:

- на команду — сообщение с инлайн-клавиатурой из `period.Kinds()`, `callback_data` = `closed:<kind>`;
- на callback — сразу `AnswerCallbackQuery` (иначе кнопка «крутится»), затем `report.Closed`
  и отправка результата; ошибка → сообщение пользователю + `logger.ERROR`.

`internal/bot/handler/view.go` — форматирование (по образцу `cmd/parser/main_test.go`: тексты
покрываются обычными unit-тестами):

```
Закрытые периоды · Сентябрь 2026
Срез №142 от 02.09.2026 03:10
⚠ последняя загрузка пустая, показан предыдущий срез   ← только при Stale

Алматы
  Аренда
    Офис              1 200 000,00           0,00
  Итого                1 200 000,00           0,00
…
ИТОГО                  4 310 500,00     980 200,00
```

- `parse_mode: HTML`, тело в `<pre>` для моноширинного выравнивания. Имена из листа
  **обязательно экранировать** (`&`, `<`, `>`): справочники приходят из Google Sheets и содержат
  что угодно. `internal/notify` не экранирует ничего именно потому, что шлёт без `parse_mode`
  (`internal/notify/telegram.go`) — здесь так нельзя.
- Разбиение: текст собирается блоками «одно подразделение», блоки пакуются в сообщения
  ≤ 3800 символов, границу режем между строками, не внутри. Единственный блок сверх лимита
  режется по строкам.
- Пустой результат — «за период данных нет», а не пустое сообщение; `snapshot.ErrNoSnapshot` —
  «данные ещё не загружены» (это другой случай, и путать их нельзя).

## 7. `cmd/bot/main.go`

По образцу `cmd/parser/main.go`: `logger.Init` → `config.LoadBot()` → `signal.NotifyContext` →
`run(ctx, cfg)` → `os.Exit(1)` при ошибке. Внутри `run`: `db.NewPool` →
`defer pool.Close()` → `repository.NewReader(pool)` → `report.New(...)` → `bot.New(...)` →
`b.Run(ctx)`. `os.Exit` только в `main`, чтобы `defer` отработали — та же оговорка, что в
`cmd/parser/main.go`.

Отличие от одноразовых бинарников: `b.Run(ctx)` блокируется до отмены контекста, а не выходит
после одного прогона; возврат `nil` при штатной остановке по сигналу.

## 8. `internal/config` — `LoadBot`

```go
type BotConfig struct { Postgres PostgresConfig; Telegram TelegramConfig }
func LoadBot() (BotConfig, error)
```

Прецедент — `LoadPostgres` (`internal/config/config.go:111`): `Load()` потребовал бы
`SPREADSHEET_ID` и `GOOGLE_CREDENTIALS_FILE`, которые боту не нужны. **Новых переменных
окружения не появляется** — доступ хранится в БД.

## 9. Обвязка

- `go.mod`: `go get github.com/go-telegram/bot` + `github.com/jackc/pgx/v5/pgxpool` (модуль уже есть).
- `Makefile`: `dev-bot: ## запустить бота локально` → `go run ./cmd/bot`. `build` подхватит
  `cmd/bot` сам (`./cmd/...`). `test-integration` расширить на новые пакеты репозитория.
- `Dockerfile` — **менять не нужно**: `RUN go build -o /out/ ./cmd/...` уже собирает всё,
  и комментарий в нём прямо это предусматривает.
- `docker-compose.yml`: сервис `bot` — тот же образ, `command: /app/bot`, `env_file: ./.env`,
  `environment: POSTGRES_HOST: postgres`, `depends_on: postgres (service_healthy)`,
  `restart: unless-stopped`. Шаблон уже лежит закомментированным в конце файла.
- `.github/workflows/release.yml`: включить `-race` в `go test` — комментарий в workflow говорит
  «появится разделяемое состояние между горутинами — включить», бот и есть этот случай.
- `docs/queries.md`: SQL для выдачи доступа (`INSERT INTO users …`) и для правки исключений
  (`UPDATE settings SET value = …, updated_at = now() WHERE key = 'closed_reports'`).
- `CLAUDE.md`: `cmd/bot` перестаёт быть «ещё не написан»; раздел про команду, таблицы
  `users`/`settings`, оговорка к правилу «только DDL», `internal/lib/` в раскладке модуля,
  уточнение «бот читает только `data_current`» → «только через разрешённый `snapshot_id`».
- `docs/data-versioning.md`: то же уточнение в разделе про вью и в контракте `Reader`.

---

## Файлы

**Новые:** `cmd/bot/main.go`, `cmd/bot/main_test.go`, `internal/bot/{bot,middleware}.go`,
`internal/bot/handler/{closed_reports,view,view_test}.go`, `internal/report/service.go`,
`internal/repository/{reader,settings,users}.go` + тесты, `internal/db/pool.go`,
`internal/lib/period/{period,period_test}.go`, `internal/lib/snapshot/{snapshot,snapshot_test}.go`,
`internal/lib/money/money.go`, `migrate/000002_bot_schema.{up,down}.sql`.

**Правятся:** `internal/config/config.go`, `internal/model/model.go`, `go.mod`, `Makefile`,
`docker-compose.yml`, `.github/workflows/release.yml`, `docs/queries.md`,
`docs/data-versioning.md`, `CLAUDE.md`.

## Порядок работ

1. Миграция + `LoadBot` + `internal/lib/period` и `internal/lib/snapshot` (+ тесты) — фундамент
   без внешних зависимостей.
2. `db.NewPool` + `repository.Reader` (+ интеграционный тест по образцу
   `internal/repository/repository_test.go`, с `TEST_DATABASE_URL` и `t.Skip`).
3. `internal/report` — сервис.
4. `internal/bot` + `cmd/bot` + форматирование (+ unit-тесты текста).
5. Обвязка: Makefile, compose, CI, документация.

## Verification

1. `make migrate-up` → `\d users`, `\d settings`; `SELECT value FROM settings WHERE key='closed_reports'`
   отдаёт список из четырёх статей. `make migrate-down` откатывает чисто. Повторный `migrate-up`
   после правки настройки её **не затирает**.
2. `make test` — юнит-тесты `period` (переход через год: прошлый месяц от января 2026 = декабрь 2025;
   прошлый квартал от января = IV квартал прошлого года), `snapshot.Latest` (новейший непустой;
   `row_count = 0` и `NULL` пропускаются; пустой список и «все пустые» → `ErrNoSnapshot`),
   форматирования и разбиения сообщений.
3. `make test-integration TEST_DATABASE_URL=postgres://…` — `ClosedReport` на сеянном снепшоте:
   исключаемые статьи не попали в суммы; строки с `NULL` division/item видны как «—»;
   после второго синка отчёт считается по новой версии, а не по сумме двух.
   Отдельный кейс: вручную вставить свежий снепшот с `row_count = 0` — отчёт считается по
   предыдущему и приходит с пометкой `Stale`, а не пустым.
4. `make up && make dev-parser && make dev-bot` — в Telegram: `/closed_reports` показывает четыре
   кнопки; каждая отдаёт таблицу; итоги сходятся с ручным SQL из `docs/queries.md` по тому же
   периоду с тем же `NOT IN`.
5. Доступ: с чужого аккаунта команда отвечает «нет доступа»; после `INSERT INTO users` — работает.
6. Отказы: погасить Postgres (`make down`) при живом боте — команда отвечает внятной ошибкой,
   процесс не падает; поднять обратно — работает без рестарта (пул переподключается).
7. Длинный отчёт: выбрать период с максимумом данных — сообщение режется на несколько, ни одна
   строка не разорвана; имя справочника с `<` или `&` не ломает разметку.
8. `Ctrl+C` / `docker compose stop bot` — выход по `SIGTERM` без паники, пул закрыт.
9. `make check` и CI с `-race` — зелёные.

---

## Что вышло по факту

Реализовано по плану, с четырьмя отступлениями.

**Разметка ответа стала rich, а не одним `<pre>`.** Заголовки, названия подразделений и итоги —
форматированный текст (`<b>`, `<i>`), он переносится по ширине экрана; строки таблицы остались
в `<pre>`, потому что только он держит колонки и прокручивается вбок вместо переноса — у `<code>`
длинная строка сворачивается и таблица теряет смысл. Разбиение переехало с блоков на секции:
разрезанное подразделение продолжается под своим заголовком с пометкой «(продолжение)».

**Отдельного `internal/bot/middleware.go` нет** — проверка доступа осталась в `bot.go` рядом
с конструктором, который её подключает: разносить сорок строк по файлам незачем.

**`cmd/bot/main_test.go` не понадобился** — в отличие от `cmd/parser`, тексты живут не в `main`,
а в `internal/bot/handler`, и покрыты там же.

**`-race` включён только в CI.** Локально `make test-integration` его не получил: детектор требует
cgo, а gcc на машине разработки нет, и цель просто перестала бы запускаться.

Ещё две мелочи: точное сложение сумм оформлено как `money.Sum` (big.Int) — итог по 70 тыс.
проводок выходит за точный диапазон `float64`; в `internal/db.Pool` добавлен `Ctx` по образцу
`Conn`.

### Проверено

`make test` (юниты), интеграционные тесты против одноразового Postgres, сквозной путь
пул → сервис → отчёт на сеянных данных: откат на непустой срез сработал (взят срез 25 вместо
пустого 26, шапка помечена), исключённая статья «Пополнение» в сводку не попала. `migrate down`
сносит таблицы бота начисто, повторный `up` не затирает отредактированную настройку.

Не проверено вживую: отправка в Telegram (нужен рабочий токен) и `-race` (нет gcc локально; в CI
отработает).
