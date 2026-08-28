FROM golang:1.27.0-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o /bin/bot      ./cmd/bot/...
RUN go build -o /bin/parser   ./cmd/parser/...
RUN go build -o /bin/migrator ./cmd/migrator/...

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata curl

COPY --from=builder /bin/bot      /bin/bot
COPY --from=builder /bin/parser   /bin/parser
COPY --from=builder /bin/migrator /bin/migrator
