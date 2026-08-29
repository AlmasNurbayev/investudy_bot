// Package migrations вшивает SQL-миграции в бинарник мигратора.
//
// Директива встраивания не умеет смотреть в родительские каталоги, поэтому пакет
// объявлен прямо здесь, рядом с .sql. На работу golang-migrate CLI это не влияет —
// `migrate -path migrate` по-прежнему видит те же файлы.
package migrations

import "embed"

// FS — все миграции в формате golang-migrate (NNNNNN_name.up.sql / .down.sql).
//
//go:embed *.sql
var FS embed.FS
