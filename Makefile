# Единый .env из корня подхватывается автоматически: бинарники читают настройки
# из окружения, и держать их для make отдельно — верный способ рассинхронизации.
# Оговорка: make трактует `#` как начало комментария, поэтому значение с решёткой
# (например пароль) нужно закавычить в .env.
ifneq (,$(wildcard .env))
include .env
export
endif

# Каталоги с кодом перечислены явно, а не через «.»: обход всего репозитория
# спотыкается на _volume_db — файлы базы (bind-mount из docker-compose.yml)
# принадлежат root с правами 0700, и gofmt падает с «permission denied»,
# пока стенд поднят.
GO_DIRS := ./cmd ./internal ./migrate

COMPOSE := docker compose -f docker-compose.yml
IMAGE   ?= investudy_bot:latest
SCHEME  ?= monthly
PKG     ?= ./...

.DEFAULT_GOAL := help

.PHONY: help
help: ## показать список целей
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# --- разработка ------------------------------------------------------------

.PHONY: dev-parser
dev-parser: ## один прогон парсера (загрузка среза из Google Sheets)
	go run ./cmd/parser

.PHONY: dev-bot
dev-bot: ## запустить бота локально (демон, до Ctrl+C)
	go run ./cmd/bot

.PHONY: migrate-up
migrate-up: ## накатить миграции
	go run ./cmd/migrator -typeTask up

.PHONY: migrate-down
migrate-down: ## откатить последнюю миграцию
	go run ./cmd/migrator -typeTask down

.PHONY: prune
prune: ## почистить историю снепшотов (SCHEME=monthly)
	go run ./cmd/prunedb -scheme $(SCHEME)

# --- сборка ----------------------------------------------------------------

.PHONY: build
build: ## собрать все бинарники в _bin/
	go build -o _bin/ ./cmd/...

.PHONY: docker-build
docker-build: ## собрать образ (IMAGE=investudy_bot:latest)
	docker build -t $(IMAGE) .

# --- проверки --------------------------------------------------------------

.PHONY: test
test: ## тесты без БД (интеграционные пропускаются)
	go test $(PKG)

.PHONY: test-integration
test-integration: ## тесты с БД; нужен TEST_DATABASE_URL
	@test -n "$(TEST_DATABASE_URL)" || { \
		echo "TEST_DATABASE_URL не задан."; \
		echo ""; \
		echo "Переменная намеренно НЕ выводится из .env: тесты начинают с TRUNCATE,"; \
		echo "и прогон против боевой базы стёр бы рабочие данные. Адрес указывается явно:"; \
		echo ""; \
		echo "  make test-integration TEST_DATABASE_URL=postgres://user:pass@localhost:5432/db"; \
		exit 1; }
	TEST_DATABASE_URL='$(TEST_DATABASE_URL)' go test ./internal/repository/

.PHONY: lint
lint: ## golangci-lint
	golangci-lint run ./...

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: fmt
fmt: ## отформатировать код
	gofmt -w $(GO_DIRS)

.PHONY: check
check: fmt vet lint test ## формат, vet, линтер и тесты разом

# --- продуктив -------------------------------------------------------------

.PHONY: up
up: ## поднять стенд (Postgres)
	$(COMPOSE) up -d

.PHONY: down
down: ## погасить стенд (данные в _volume_db остаются)
	$(COMPOSE) down

.PHONY: ps
ps: ## состояние сервисов
	$(COMPOSE) ps

.PHONY: logs
logs: ## логи стенда
	$(COMPOSE) logs -f
