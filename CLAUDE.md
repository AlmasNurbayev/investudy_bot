# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Telegram bot providing financial information to users. Data flows from Google Sheets → PostgreSQL → Telegram bot (Go).

Two separate components:
- **parser/** — Go service that pulls data from Google Sheets and writes it to PostgreSQL
- **bot/** — Go Telegram bot that reads from PostgreSQL and serves user queries

## Architecture

```
Google Sheets
     │
     ▼
parser/ (Go)          ← uses Google Sheets API v4
     │
     ▼
PostgreSQL            ← shared database
     │
     ▼
bot/ (Go)             ← uses github.com/go-telegram/bot
     │
     ▼
Telegram users
```

### parser/
Fetches spreadsheet data on a schedule (cron or ticker), transforms rows into domain structs, upserts into PostgreSQL. Credentials via Google service account JSON (path in env).

### bot/
Telegram bot на `github.com/go-telegram/bot`. Регистрация хендлеров через `bot.RegisterHandler`, запуск через `b.Start(ctx)`. Обрабатывает команды пользователей, читает из PostgreSQL, отправляет ответы. Stateless — всё состояние в БД.

### Database
Драйвер — `pgx` (`github.com/jackc/pgx/v5`). Миграции — `golang-migrate/migrate` с pgx-драйвером. Файлы миграций в `migrate/` в формате `000001_name.up.sql` / `000001_name.down.sql`.

## Environment Variables

Все переменные хранятся в едином `.env` в корне репозитория и загружаются обоими сервисами.

| Variable | Description |
|---|---|
| `SPREADSHEET_ID` | ID документа Google Sheets |
| `SHEET_NAME` | Название листа (например, `ДДС`) |
| `GOOGLE_CREDENTIALS_FILE` | Путь к service account JSON |
| `POSTGRES_HOST` | Хост PostgreSQL |
| `POSTGRES_PORT` | Порт PostgreSQL |
| `POSTGRES_DB` | Имя базы данных |
| `POSTGRES_USER` | Пользователь |
| `POSTGRES_PASSWORD` | Пароль |
| `TELEGRAM_BOT_TOKEN` | Токен от @BotFather |

## Development (make)

Для локальной разработки используется `Makefile`.

```bash
make dev-bot      # запустить бота (go run)
make dev-parser   # запустить парсер (go run)
make migrate      # migrate -path migrate -database $DATABASE_URL up
make migrate-down # откат последней миграции
make test         # go test ./...
make test-pkg PKG=./bot/internal/handler/...  # тесты одного пакета
make lint         # golangci-lint run ./...
```

## Deploy (Docker)

Оба бинарника собираются в **один образ** через многоэтапный `Dockerfile`:

```dockerfile
# stage 1 — сборка
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o /bin/bot    ./bot/cmd/...
RUN go build -o /bin/parser ./parser/cmd/...

# stage 2 — финальный образ
FROM alpine:3.20
COPY --from=builder /bin/bot    /bin/bot
COPY --from=builder /bin/parser /bin/parser
```

Запуск в продуктиве через `docker-compose.yml` — оба сервиса используют один образ, но разные `command`:

```yaml
services:
  bot:
    image: investudy_bot
    command: /bin/bot
    env_file: .env

  parser:
    image: investudy_bot
    command: /bin/parser
    env_file: .env

  postgres:
    image: postgres:16-alpine
    env_file: .env
```

```bash
docker compose up -d        # поднять в продуктиве
docker compose build        # пересобрать образ
docker compose logs -f bot  # логи бота
```

## Module Layout (planned)

```
investudy_bot/
├── bot/
│   ├── cmd/main.go
│   └── internal/
│       ├── handler/   # command handlers (per Telegram command)
│       ├── repo/      # PostgreSQL queries
│       └── service/   # business logic
├── parser/
│   ├── cmd/main.go
│   └── internal/
│       ├── sheets/    # Google Sheets client + fetcher
│       ├── repo/      # PostgreSQL upsert logic
│       └── model/     # shared domain structs
├── migrate/           # SQL migration files (golang-migrate format)
├── Dockerfile
├── docker-compose.yml
├── Makefile
└── go.work            # Go workspace linking bot/ and parser/ modules
```

## Data Source Structure (Google Sheets → PostgreSQL)

Лист **ДДС** (движение денежных средств) — банковские транзакции. 24 колонки:

| Поле (англ.) | Русское название | Тип / формат |
|---|---|---|
| `date` | Дата | `DD.MM.YYYY` |
| `numOper` | # | строка |
| `typeOper` | Тип | строка |
| `debetVal` | Дебет валюта | decimal |
| `creditVal` | Кредит валюта | decimal |
| `exRate` | Курс | decimal |
| `debet` | Дебет | decimal, запятая как разделитель; исключает `credit` |
| `credit` | Кредит | decimal, запятая как разделитель; исключает `debet` |
| `sender` | Бенеф-р/отправитель | контрагент + ИИН/БИН |
| `description` | Назначение платежа | свободный текст |
| `bank` | Банк | короткое имя (`Kaspi`, `Halyk`, …) |
| `period` | Период | `01.MM.YYYY` — первое число месяца |
| `organization` | Организация | юр. лицо (`Aligee`) |
| `division_id` | Подразделение | FK → `divisions(id)` |
| `item_id` | Статья | FK → `items(id)` |
| `sub_item_id` | Подстатья | FK → `sub_items(id)` (sub_items.item_id → items) |
| `comment1` | Учет | |
| `comment2` | Комментарий | |
| `fin_type_id` | Тип | FK → `fin_types(id)` (`доход` / `расход` / `возврат` и др.) |
| `sumDash` | СуммаДаш | decimal |
| `vid_id` | Вид | FK → `vids(id)` (центр затрат) |
| `sumRevenue` | СуммаДоход | decimal; заполнен только для `type=доход` |
| `sumCost` | СуммаРасход | decimal; заполнен только для `type=расход` |
| `sumReturn` | СуммаВозврат | decimal; заполнен только для `type=возврат` |

**Справочные таблицы:** `divisions`, `items`, `sub_items`, `fin_types`, `vids` — все FK nullable. Парсер делает `INSERT … ON CONFLICT(name) DO NOTHING RETURNING id` перед вставкой в `data`. Для `sub_items` сначала upsert родительского `item`, затем `sub_item` с `item_id`.

Особенности парсинга:
- `debet`/`credit` — взаимоисключающие: в строке заполнено только одно.
- `sumRevenue`, `sumCost`, `sumReturn` — только одно ненулевое на строку в зависимости от `type`.
- Числа в формате `46829,00` — при парсинге заменять `,` → `.` перед `strconv.ParseFloat`.
- `date` и `period` парсить через `time.Parse("02.01.2006", v)`.

## Key Design Decisions

- Два бинарника в одном Docker-образе — упрощает сборку и деплой, но сервисы управляются независимо через docker-compose `command`.
- `go.work` workspace allows both modules to share local packages (e.g. `model/`) without publishing.
- PostgreSQL is the single source of truth; the parser is the only writer, the bot is read-only.
- Google Sheets credentials must never be committed — use env var pointing to a file outside the repo.
- Зависимости передаются через интерфейсы — репозитории и внешние клиенты описываются интерфейсом в пакете-потребителе, конкретная реализация передаётся через конструктор.
