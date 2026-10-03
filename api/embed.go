// Package apispec вшивает контракт API в бинарник: cmd/api раздаёт его
// Swagger UI из того же файла, по которому сгенерирован сервер.
//
// Директива встраивания не умеет смотреть в родительские каталоги, поэтому
// пакет объявлен прямо здесь, рядом с openapi.yaml — как migrate/embed.go.
package apispec

import _ "embed"

// OpenAPI — api/openapi.yaml как есть.
//
//go:embed openapi.yaml
var OpenAPI []byte
