# Версионирование таблицы `data`: еженедельные снепшоты

## Context

Данные приезжают из Google Sheets (лист ДДС) в PostgreSQL и читаются ботом. Сейчас `data` —
плоская таблица без бизнес-ключа, без индексов кроме суррогатного PK и без какой-либо истории
(`migrate/000001_init_schema.up.sql`). Парсер ещё не реализован — есть только подключение
к БД (`cmd/parser/main.go`, `internal/db/repository.go`), поэтому схему можно закладывать сразу
правильно, без миграции существующих данных.

Нужно:
- синк из Sheets **ежедневно**;
- хранить **по одному срезу в неделю** как историю;
- читать историю как обычные данные: веб-отчёт с агрегацией и фильтрами по справочникам
  (`divisions`, `items`, `sub_items`, `fin_types`, `vids`);
- уметь сравнивать **один отчётный период в двух версиях по времени** (при разборе расхождений).

Объём: ~40 тыс. строк сейчас, ~70 тыс. к концу года. При еженедельном удержании это
~3.6 млн строк в год — для Postgres нормально при правильных индексах.

### Почему одна таблица, а не `data` + `data_snapshots`

Раз история — это такой же путь чтения, как и текущее состояние, то при двух таблицах **каждый**
отчётный запрос пришлось бы писать дважды или через UNION, а сравнение «текущее против архивного»
стало бы кросс-табличным. Одна таблица с версией даёт один код-путь: `snapshot_id` — просто
параметр запроса, а сравнение версий выражается одним `WHERE snapshot_id IN ($1, $2)`.

### Почему реестр `snapshots`, а не флаг `is_current` в `data`

Флаг требовал бы ежедневного `UPDATE ... SET is_current = false` по всем 70 тыс. строк (в Postgres
это физическая перезапись каждой строки) и не хранил бы метаданных версии. Реестр решает и то, и
другое: «текущая версия» — вычисляемая величина (максимум `taken_at`), так что публикация не
трогает вообще ни одной строки и сводится к `COMMIT`, а у веб-интерфейса появляется готовый
список версий для селектора.

---

## Схема

Единая миграция `migrate/000001_init_schema.up.sql` / `.down.sql`: `snapshots` и
`data.snapshot_id` связаны обязательным FK, поэтому создаются вместе. Миграции
содержат только DDL и не трогают данные.

```sql
CREATE TABLE IF NOT EXISTS snapshots (
    id        BIGSERIAL   PRIMARY KEY,
    taken_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    row_count INTEGER,                         -- опционально, см. ниже

    -- витрина для селектора версии «год → месяц → неделя» в вебе
    year  SMALLINT GENERATED ALWAYS AS (EXTRACT(YEAR  FROM (taken_at AT TIME ZONE 'UTC'))) STORED,
    month SMALLINT GENERATED ALWAYS AS (EXTRACT(MONTH FROM (taken_at AT TIME ZONE 'UTC'))) STORED,
    week  SMALLINT GENERATED ALWAYS AS (EXTRACT(WEEK  FROM (taken_at AT TIME ZONE 'UTC'))) STORED
);

CREATE INDEX snapshots_taken_at_idx ON snapshots (taken_at DESC);
CREATE INDEX snapshots_picker_idx   ON snapshots (year DESC, month DESC, week DESC);

-- внутри CREATE TABLE data, сразу после id:
    snapshot_id BIGINT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,

-- каждый отчётный запрос начинается с snapshot_id → он первым в композитных индексах
CREATE INDEX data_snapshot_period_idx ON data (snapshot_id, period);
CREATE INDEX data_snapshot_date_idx   ON data (snapshot_id, date);
```

Про `year` / `month` / `week`. Это **производные от `taken_at`**, а не самостоятельные данные,
поэтому они объявлены `GENERATED ALWAYS ... STORED`: парсер их не заполняет и рассинхронизировать
с `taken_at` физически не может. `AT TIME ZONE 'UTC'` здесь обязателен — `EXTRACT` от `timestamptz`
зависит от `TimeZone` сессии и не является IMMUTABLE, так что без явной зоны Postgres откажется
создавать generated-колонку. Требуется PostgreSQL 12+.

`week` — это номер ISO-недели, и он согласован с правилом удержания: раз в неделе остаётся ровно
один снепшот, номера недель внутри выбранного месяца никогда не столкнутся, и селектор
однозначен. Известная особенность: срез, снятый в последних числах декабря, может попасть в
ISO-неделю 1 следующего года и покажется в декабре как «неделя 1». Это безобидно, пока `week`
используется **внутри уже выбранного месяца**, а не как глобальный ключ — под это и заточен
индекс `snapshots_picker_idx`. Правило удержания на эти колонки не опирается (см. ниже) и
остаётся самодостаточным.

Отдельного `created_at` в `data` нет: он дублировал бы `snapshots.taken_at` — время у всех
строк среза одно.

### Вью «текущая версия»

```sql
CREATE OR REPLACE VIEW data_current AS
SELECT d.*
FROM data d
WHERE d.snapshot_id = (SELECT id FROM snapshots ORDER BY taken_at DESC LIMIT 1);
```

Фильтра «только опубликованные» здесь нет и не нужно: строка `snapshots` создаётся в той же
транзакции, что и залив, поэтому до `COMMIT` она невидима другим сессиям — недостроенный срез
не может стать «текущим» физически.

Бот читает **только `data_current`** — его запросы выглядят так, будто версионирования нет, и
забыть фильтр невозможно. Веб-отчёт, которому нужна произвольная версия, читает `data` с явным
`snapshot_id`.

### Справочники не версионируются

`divisions` / `items` / `sub_items` / `fin_types` / `vids` апсертятся по имени и никогда не
удаляются, id не переиспользуются — значит FK из старых снепшотов остаются валидными вечно.

Апсерт пишется как `ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id`, а не
через `DO NOTHING RETURNING id`: последний на конфликте не возвращает ни одной строки, то есть
для уже существующего значения id не придёт.

Известное ограничение: переименование значения в исходном листе создаст **новую** строку
справочника, и в фильтрах веб-отчёта старые снепшоты будут числиться под старым именем. Решать
сейчас не нужно; если всплывёт — лечится таблицей алиасов.

---

## Цикл парсера

Одна транзакция (`internal/db/repository.go` уже даёт `Begin`/`Commit`/`Rollback`):

1. `INSERT INTO snapshots DEFAULT VALUES RETURNING id`.
2. Upsert справочников, резолв FK-id (для `sub_items` — сперва родительский `item`).
3. Заливка всех строк через `pgx.CopyFrom` с проставленным `snapshot_id`.
4. `UPDATE snapshots SET row_count = $n WHERE id = $id` — одна строка. Поле опциональное, но
   даёт дешёвый инвариант для мониторинга («лист внезапно похудел вдвое»).
5. `COMMIT` — публикация атомарна: до коммита `data_current` отдаёт прошлую версию, после —
   новую. Читатели ни на миг не блокируются и не видят полузалитый срез.

Флаг состояния тут не нужен: MVCC скрывает всю недостроенную версию целиком, а падение
откатывает и строку `snapshots` — мусора после себя не оставляет, подчищать нечего.

Обратите внимание: **бизнес-ключ не нужен**. У строк листа его нет, и при полной перезаливке
среза он не требуется — сматчивание строки с её вчерашней версией не происходит.

## Удержание: «новейший снепшот каждой ISO-недели»

Отдельный крон не нужен — правило выполняется после успешного синка, своей транзакцией:

```sql
DELETE FROM snapshots s
WHERE EXISTS (
    SELECT 1 FROM snapshots n
    WHERE n.taken_at > s.taken_at
      AND date_trunc('week', n.taken_at AT TIME ZONE 'UTC')
        = date_trunc('week', s.taken_at AT TIME ZONE 'UTC')
);
```

`AT TIME ZONE 'UTC'` обязателен: `date_trunc('week', <timestamptz>)` иначе зависит от `TimeZone`
сессии, и граница недели поехала бы между парсером и psql-сессией администратора.

Как это даёт ровно то, что нужно: во вторник вчерашний (понедельничный) срез перестаёт быть
новейшим в своей неделе и удаляется; в понедельник новый срез открывает новую неделю, а
воскресный остаётся новейшим в прошлой и сохраняется навсегда. Итог — текущая версия плюс по
одной на каждую прошедшую неделю. Если сервис лежал несколько дней, правило само себя чинит.

`ON DELETE CASCADE` уносит строки `data` — прунинг остаётся одним запросом по реестру.

Возрастное удержание (глубина истории) стоит завести сразу как env-параметр
`SNAPSHOT_RETENTION_WEEKS` (0 = хранить всё), даже если пока выставить 0:

```sql
DELETE FROM snapshots
WHERE taken_at < now() - ($1 || ' weeks')::interval
  AND id <> (SELECT id FROM snapshots ORDER BY taken_at DESC LIMIT 1);
```

---

## Код

Зависимости — через интерфейсы в пакете-потребителе (конвенция из `CLAUDE.md`).

**Запись** (`parser/internal/repo`, потребляется `cmd/parser`):

```go
type SnapshotStore interface {
    BeginSnapshot(ctx context.Context) (snapshotID int64, err error)
    CopyRows(ctx context.Context, snapshotID int64, rows []model.Row) (int64, error)
    // фиксирует row_count; публикация происходит самим COMMIT транзакции
    FinishSnapshot(ctx context.Context, snapshotID int64, rowCount int64) error
    Prune(ctx context.Context, retainWeeks int) error
}
```

**Чтение** (`bot/internal/repo`; тот же интерфейс переиспользуется будущим веб-бэкендом):

```go
type Reader interface {
    LatestSnapshot(ctx context.Context) (model.Snapshot, error)
    // для селектора версии: year/month/week приходят готовыми, без вычислений на стороне Go
    ListSnapshots(ctx context.Context, limit int) ([]model.Snapshot, error)
    // snapshotID == 0 → data_current; иначе конкретная версия. Один метод на оба случая.
    Report(ctx context.Context, snapshotID int64, f model.Filter) ([]model.Aggregate, error)
}
```

Сравнение версий одного периода — не отдельный слой, а тот же `Report` с группировкой по версии:

```sql
SELECT snapshot_id, item_id, sum(sum_cost), sum(sum_revenue)
FROM data
WHERE snapshot_id IN ($1, $2) AND period = $3
GROUP BY snapshot_id, item_id;
```

Новая env-переменная (в `.env` и таблицу в `CLAUDE.md`): `SNAPSHOT_RETENTION_WEEKS`.

Расписания в конфиге нет: парсер одноразовый — один синк и выход, а запускает его
крон. Ненулевой код возврата — сигнал крону о неудаче.

## Файлы

- `migrate/000001_init_schema.up.sql` / `.down.sql` — схема целиком
- `parser/internal/repo/` — реализация `SnapshotStore` (+ upsert справочников)
- `parser/internal/sheets/` — фетч листа (пока не существует)
- `cmd/parser/main.go` — один прогон «снепшот → залив → publish → prune» и выход
- `internal/config` — `SNAPSHOT_RETENTION_WEEKS`
- `CLAUDE.md` — раздел про версионирование, обновлённая таблица env

---

## Verification

1. `make migrate` (или `go run ./cmd/migrator`) → `\d data` показывает `snapshot_id`,
   `\d+ data_current` — вью; `make migrate-down` откатывает чисто.
2. Прогнать синк дважды подряд: `SELECT count(*) FROM data_current` равен `row_count` новейшей
   версии, а не сумме двух.
3. Прунинг в рамках одной недели: после второго прогона старая версия исчезла
   (`SELECT count(*) FROM snapshots` = 1), и вместе с ней её строки в `data` (CASCADE).
4. Граница недели: вручную выставить существующему снепшоту `taken_at` на прошлую неделю,
   прогнать синк — он должен **сохраниться**, а сегодняшний предыдущий — исчезнуть.
5. Атомарность публикации: в одной сессии открыть транзакцию синка и не коммитить; во второй
   `SELECT count(*) FROM data_current` — отдаёт старую версию и не блокируется.
6. Сравнение версий: запрос с `snapshot_id IN (...)` по одному `period` возвращает по строке
   на версию.
7. Селектор: `SELECT id, year, month, week FROM snapshots ORDER BY taken_at DESC` — колонки
   заполнены сами, парсер их не писал; попытка `UPDATE snapshots SET year = 1999` падает с
   ошибкой (колонка generated), то есть рассинхрон невозможен.
8. План запросов: `EXPLAIN (ANALYZE)` отчётного запроса использует `data_snapshot_period_idx`,
   а не Seq Scan на 70 тыс. строк × число версий.
9. `make test` / `make lint`.

## Отложено сознательно

- **Партиционирование** `data` по `snapshot_id` — при 3.6 млн строк в год не нужно; вернуться,
  когда таблица подойдёт к ~50 млн. Схема к этому готова: ключ партиционирования уже есть.
- **Дедупликация неизменившихся строк** (SCD-2 по хешу) — сложнее на порядок и требует
  бизнес-ключа, которого у данных нет. Полные копии при таком объёме дешевле.
