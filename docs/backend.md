# Бэкенд

REST API (`cmd/api`, fiber). Исходные пожелания — fiber + swagger, ручки
`auth/login`, `reports/month_pnl`, `reports/week_pnl` и т. д.

**Заменено [plans/pnl-architecture.md](plans/pnl-architecture.md):**

- контракт — OpenAPI-first (`api/openapi.yaml` → `oapi-codegen`), а не swag-аннотации:
  Swagger UI раздаётся из того же файла;
- саморегистрации нет — пользователей заводит администратор;
- список ручек и форма ответа — §4 плана;
- данные отдаются в соответствии с уровнем доступа — политика одна на бота и API,
  `internal/access`.
