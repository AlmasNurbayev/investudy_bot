## О проекте

Сервис для загрузки, хранения и отдачи финансовой информации. Данные идут по цепочке
Google Sheets → PostgreSQL → Telegram-бот (Go) и REST API с веб-интерфейсом.

Пять бинарников, один общий Docker-образ:

| Бинарник          | Роль                                         | Как живёт                         |
| ----------------- | -------------------------------------------- | --------------------------------- |
| **cmd/parser/**   | загрузка среза из Google Sheets в PostgreSQL | одноразовый, запускается кроном   |
| **cmd/migrator/** | миграции БД (golang-migrate)                 | одноразовый, стартует до сервисов |
| **cmd/prunedb/**  | чистка истории снепшотов (`-scheme monthly`) | одноразовый, запускается кроном   |
| **cmd/bot/**      | Telegram-бот, читает из PostgreSQL           | демон                             |
| **cmd/api/**      | REST API на fiber, читает из PostgreSQL      | демон, проектируется              |

## Веб-ОПиУ

Рядом с ботом появляются REST-бэкенд (`cmd/api`) и SPA на React: отчёт о прибылях и
убытках по денежным операциям — месячный, квартальный, годовой и оперативный недельный,
дашборд, детальные данные с выгрузкой в XLSX/PDF и админка пользователей. Кода пока нет,
решения приняты.

- [docs/plans/pnl-architecture.md](docs/plans/pnl-architecture.md) — принятые решения, схема данных, контракт API. Главный документ: где более ранние расходятся с ним, верен он.
- [docs/pnl-structure.md](docs/pnl-structure.md) — строки отчёта, отбор по статьям, границы доступа по ролям.
- [docs/frontend.md](docs/frontend.md), [docs/backend.md](docs/backend.md) — исходные требования к сайту и API.
- [docs/plans/pnl-web.md](docs/plans/pnl-web.md) — рабочий документ обсуждения; вход через Telegram Login Widget и роли в нём устарели.

Доступ к отчёту — по ролям, до определённой строки включительно: руководитель отдела
видит свой разрез до валовой прибыли, операционный директор — до операционной,
коммерческий — до чистой, учредитель и финансовый директор — всё.

## Документация

- [CLAUDE.md](CLAUDE.md) — архитектура и принятые решения
- [CONTEXT.md](CONTEXT.md) — словарь терминов проекта
- [docs/parser.md](docs/parser.md) — особенности разбора листа
- [docs/data-versioning.md](docs/data-versioning.md) — версионирование `data` снепшотами
- [docs/deploy.md](docs/deploy.md) — деплой
- [docs/queries.md](docs/queries.md) — ручные SQL-запросы

## Разработка

```bash
make help              # список целей
make dev-parser        # один прогон парсера
make migrate-up        # накатить миграции
make build             # бинарники в _bin/
make check             # fmt + vet + lint + test
make up / down / logs  # стенд из docker-compose.yml
```

## TODO

- [ ] Выдачу /closed_reports изменить на rich messages
- [x] При запуске парсера из контейнера не видит JSON, так как не примонтирован — подвязать папку
