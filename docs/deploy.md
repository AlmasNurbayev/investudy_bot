## Деплой (Docker)

Все бинарники собираются в один образ (`go build -o /out/ ./cmd/...` — команды подхватываются автоматически, перечислять поимённо нельзя, иначе сборка падает на ещё не написанной). Продуктив — `docker-compose.yml` в корне репозитория, пока только Postgres.

Схему накатывает сам сервис при старте цепочкой в `CMD`, поэтому мигратор гарантированно отрабатывает раньше и `depends_on` не нужен:

```dockerfile
CMD ["sh", "-c", "/app/migrator -typeTask up && exec /app/parser"]
```

`exec` обязателен: без него `SIGTERM` от `docker stop` дойдёт до `sh`, а не до сервиса.

```bash
make up            # поднять Postgres
make logs          # логи
docker build -t investudy_bot:latest .
```

**Запускается только корневой `docker-compose.yml`** — на него указывает `COMPOSE` в `Makefile`, им же смонтированы `pg_conf/postgresql.conf` и каталог данных `_volume_db/`. Каталог `deploy/` держится как справочный образец того же файла: пути в нём заданы относительно `deploy/`, поэтому `docker compose -f deploy/docker-compose.yml up` поднимет **второй** стек с другим каталогом данных.

Разъезд этих двух копий не просто дублирует контейнер, а ломает базу молча. Postgres инициализирует кластер один раз — entrypoint выполняет `initdb`, дописывает в `pg_hba.conf` строку `host all all all scram-sha-256` и создаёт `POSTGRES_DB` единым блоком, который пропускается целиком, если в PGDATA уже лежит `PG_VERSION`. Прерванный первый старт оставляет кластер, где `initdb` прошёл, а всё остальное — нет; дальше entrypoint печатает `Skipping initialization`, и база с правилом доступа не появятся уже никогда. Снаружи это выглядит как `no pg_hba.conf entry for host …` и `database "investudy_bot" does not exist` при живом и «здоровом» контейнере. Лечится только сносом каталога данных и чистой инициализацией — правка `.env` или `pg_hba.conf` тут бесполезна.

Файлы базы — bind-mount `_volume_db` в корне репозитория. **В Postgres 18 каталог данных переехал**: `PGDATA=/var/lib/postgresql/18/docker`, том объявлен на `/var/lib/postgresql`. Привычный по старым примерам монтаж на `.../data` промахнётся мимо данных, и они уедут в анонимный том.