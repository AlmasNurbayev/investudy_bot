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

    -- Витрина для селектора версии «год → месяц → неделя».
    --
    -- Зона указана явно по двум причинам. Во-первых, EXTRACT от timestamptz без
    -- неё зависит от TimeZone сессии и не является IMMUTABLE, то есть в
    -- generated-колонку не проходит. Во-вторых, она должна быть местной, а не UTC:
    -- Алматы это UTC+5, поэтому ночной прогон кроном (до 05:00) в UTC попал бы на
    -- предыдущие сутки — понедельничный срез записался бы в воскресную неделю,
    -- а срез 1 марта — в февраль.
    --
    -- На эти же колонки опирается схема чистки monthly (cmd/prunedb), поэтому
    -- граница месяца в отчёте и в удалении совпадает по построению.
    -- Вторая копия зоны — timezone в deploy/pg_conf/postgresql.conf.
    year  SMALLINT GENERATED ALWAYS AS (EXTRACT(YEAR  FROM (taken_at AT TIME ZONE 'Asia/Almaty'))) STORED,
    month SMALLINT GENERATED ALWAYS AS (EXTRACT(MONTH FROM (taken_at AT TIME ZONE 'Asia/Almaty'))) STORED,
    week  SMALLINT GENERATED ALWAYS AS (EXTRACT(WEEK  FROM (taken_at AT TIME ZONE 'Asia/Almaty'))) STORED
);

CREATE INDEX IF NOT EXISTS snapshots_taken_at_idx ON snapshots (taken_at DESC);
CREATE INDEX IF NOT EXISTS snapshots_picker_idx   ON snapshots (year DESC, month DESC, week DESC);

-- Проводки листа ДДС. Каждая строка принадлежит версии среза, поэтому
-- snapshot_id обязателен: строка без версии не имеет смысла.
-- Точность денежных колонок задана явно, и число 17 выбрано не случайно.
--
-- Парсер держит суммы в float64, а он точен ровно до 17 значащих цифр: с 18-й
-- начинается тихое искажение (1234567890123456.78 превращается в ...456.8 без
-- всякой ошибки). NUMERIC(17,2) больше 17 цифр не принимает вовсе — значит любое
-- значение, прошедшее в колонку, float64 представляет точно, а всё остальное
-- отвергается громкой ошибкой. Потолок при этом ~10^15 на проводку.
--
-- Второй эффект — сохранение масштаба: без указания scale «46829,00» легло бы
-- как 46829, и отчёты показывали бы разное число знаков в соседних строках.
-- Масштаб теряется независимо от типа в Go, это свойство NUMERIC без scale.
--
-- Оговорка: третий знак молча округляется (1000000.005 → 1000000.01). Для сумм
-- с двумя знаками это доменное правило, но округление именно молчаливое.
CREATE TABLE IF NOT EXISTS data (
    id           BIGSERIAL PRIMARY KEY,
    snapshot_id  BIGINT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    date         DATE,
    num_oper     TEXT,
    type_oper    TEXT,
    debet_val    NUMERIC(17,2),
    credit_val   NUMERIC(17,2),
    ex_rate      NUMERIC(17,8),   -- курс требует больше знаков, чем суммы
    debet        NUMERIC(17,2),
    credit       NUMERIC(17,2),
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
    sum_dash     NUMERIC(17,2),
    vid_id       INT REFERENCES vids(id),
    sum_revenue  NUMERIC(17,2),
    sum_cost     NUMERIC(17,2),
    sum_return   NUMERIC(17,2)
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
