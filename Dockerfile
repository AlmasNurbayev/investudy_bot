FROM golang:1.27.0-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Собираются все команды из cmd/: имя бинарника берётся из имени пакета.
# Перечислять их поимённо нельзя — go build падает на ещё не написанной команде,
# а так cmd/bot и cmd/migrator подхватятся сами, когда появятся.
# Вывод — в /out, а не в /app: /app здесь WORKDIR с исходниками, и бинарники
# смешались бы с каталогами пакетов, ломая COPY на следующем этапе.
RUN go build -o /out/ ./cmd/...

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata curl

COPY --from=builder /out/* /app/

# Сервис сам накатывает схему перед стартом, поэтому мигратор гарантированно
# отрабатывает раньше, а его ненулевой код не даст сервису подняться на
# несовпадающей схеме. exec заменяет процесс шелла, иначе SIGTERM от docker stop
# доходил бы до sh, а не до сервиса, и остановка шла бы по таймауту.
#
# Адрес БД мигратор берёт из POSTGRES_* переменных; если удобнее одна строка —
# добавить `-dsn "$DSN"`. Для бота команда переопределяется в docker-compose.
CMD ["sh", "-c", "/app/migrator -typeTask up && exec /app/parser"]
