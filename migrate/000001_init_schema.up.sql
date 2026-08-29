-- Справочники. Пополняются парсером по имени, строки никогда не удаляются,
-- id не переиспользуются — поэтому FK из старых снепшотов остаются валидными.
CREATE TABLE IF NOT EXISTS divisions (
    id   SERIAL PRIMARY KEY,
    name TEXT UNIQUE NOT NULL
);

CREATE TABLE IF NOT EXISTS items (
    id   SERIAL PRIMARY KEY,
    name TEXT UNIQUE NOT NULL
);

CREATE TABLE IF NOT EXISTS sub_items (
    id      SERIAL PRIMARY KEY,
    name    TEXT UNIQUE NOT NULL,
    item_id INT REFERENCES items(id)
);

CREATE TABLE IF NOT EXISTS fin_types (
    id   SERIAL PRIMARY KEY,
    name TEXT UNIQUE NOT NULL
);

CREATE TABLE IF NOT EXISTS vids (
    id   SERIAL PRIMARY KEY,
    name TEXT UNIQUE NOT NULL
);

-- Реестр версий среза. Создаётся до data: на него ссылается обязательный FK.
CREATE TABLE IF NOT EXISTS snapshots (
    id        BIGSERIAL   PRIMARY KEY,
    taken_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    row_count INTEGER,

    -- витрина для селектора версии «год → месяц → неделя»;
    -- AT TIME ZONE 'UTC' обязателен: EXTRACT от timestamptz зависит от TimeZone
    -- сессии и без явной зоны не IMMUTABLE, то есть в generated-колонку не проходит
    year  SMALLINT GENERATED ALWAYS AS (EXTRACT(YEAR  FROM (taken_at AT TIME ZONE 'UTC'))) STORED,
    month SMALLINT GENERATED ALWAYS AS (EXTRACT(MONTH FROM (taken_at AT TIME ZONE 'UTC'))) STORED,
    week  SMALLINT GENERATED ALWAYS AS (EXTRACT(WEEK  FROM (taken_at AT TIME ZONE 'UTC'))) STORED
);

CREATE INDEX IF NOT EXISTS snapshots_taken_at_idx ON snapshots (taken_at DESC);
CREATE INDEX IF NOT EXISTS snapshots_picker_idx   ON snapshots (year DESC, month DESC, week DESC);

-- Проводки листа ДДС. Каждая строка принадлежит версии среза, поэтому
-- snapshot_id обязателен: строка без версии не имеет смысла.
CREATE TABLE IF NOT EXISTS data (
    id           BIGSERIAL PRIMARY KEY,
    snapshot_id  BIGINT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    date         DATE,
    num_oper     TEXT,
    type_oper    TEXT,
    debet_val    NUMERIC,
    credit_val   NUMERIC,
    ex_rate      NUMERIC,
    debet        NUMERIC,
    credit       NUMERIC,
    sender       TEXT,
    description  TEXT,
    bank         TEXT,
    period       DATE,
    organization TEXT,
    division_id  INT REFERENCES divisions(id),
    item_id      INT REFERENCES items(id),
    sub_item_id  INT REFERENCES sub_items(id),
    comment1     TEXT,
    comment2     TEXT,
    fin_type_id  INT REFERENCES fin_types(id),
    sum_dash     NUMERIC,
    vid_id       INT REFERENCES vids(id),
    sum_revenue  NUMERIC,
    sum_cost     NUMERIC,
    sum_return   NUMERIC
);

-- Каждый отчётный запрос начинается с snapshot_id → он первым в композитных индексах.
CREATE INDEX IF NOT EXISTS data_snapshot_period_idx ON data (snapshot_id, period);
CREATE INDEX IF NOT EXISTS data_snapshot_date_idx   ON data (snapshot_id, date);

-- Текущая версия. Фильтр «только опубликованные» не нужен: строка snapshots
-- создаётся в той же транзакции, что и залив, поэтому до COMMIT она невидима
-- другим сессиям и недостроенный срез не может стать текущим.
CREATE OR REPLACE VIEW data_current AS
SELECT d.*
FROM data d
WHERE d.snapshot_id = (SELECT id FROM snapshots ORDER BY taken_at DESC LIMIT 1);
