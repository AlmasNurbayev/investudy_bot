-- Схема веб-ОПиУ: пользователи сайта и бота, сессии, структура отчёта.
-- Решения и их причины — docs/plans/pnl-architecture.md, §3.

-- users — общая таблица бота и сайта.
--
-- Одна таблица, а не две: блокировка на сайте обязана закрывать доступ и в
-- боте, иначе отрезанный от сайта продолжал бы читать отчёт из Telegram.
-- Поэтому ключ — свой id, а telegram_id становится необязательной колонкой:
-- пользователь сайта Telegram может не иметь вовсе.
--
-- Таблица создана в 000002 и уже содержит выданные доступы, поэтому она
-- переделывается на месте, а не пересоздаётся. Существующие строки получают
-- id из последовательности и сохраняют telegram_id, username, role.
--
-- Повторный прогон файла упадёт на первом же ALTER (users_pkey к тому
-- времени — ключ по id, на него ссылается sessions) и ничего не изменит:
-- громкая ошибка вместо потери данных.
--
-- Ограничения — только ключи, UNIQUE и NOT NULL. Допустимые роли,
-- подразделение руководителя, пароль при логине проверяет код
-- (internal/access и сервис пользователей).
ALTER TABLE users DROP CONSTRAINT users_pkey;
ALTER TABLE users ADD COLUMN id BIGSERIAL PRIMARY KEY;

-- NULL — пользователь только сайта.
ALTER TABLE users ALTER COLUMN telegram_id DROP NOT NULL;
ALTER TABLE users ADD CONSTRAINT users_telegram_id_key UNIQUE (telegram_id);

-- NULL — пользователь только бота.
ALTER TABLE users ADD COLUMN login TEXT;
ALTER TABLE users ADD CONSTRAINT users_login_key UNIQUE (login);

-- argon2id в PHC-формате; NULL — войти на сайт нельзя.
ALTER TABLE users ADD COLUMN password_hash TEXT;

-- Роль задаёт последнюю видимую строку ОПиУ: division_head, coo, cco,
-- founder, cfo. Умолчание 'viewer' из 000002 снимается: такой роли политика
-- не знает, и новый пользователь без явной роли должен не вставиться, а не
-- получить роль, с которой ему ничего не покажут. Строки, уже записанные
-- с 'viewer', остаются как есть — бот роль не читает, а сайт откажет им,
-- пока администратор не назначит роль.
ALTER TABLE users ALTER COLUMN role DROP DEFAULT;

-- Подразделение руководителя отдела (у остальных NULL).
ALTER TABLE users ADD COLUMN division_id INT REFERENCES divisions(id);

-- Администратор — признак, а не роль: право заводить пользователей и
-- править структуру с глубиной отчёта не связано.
ALTER TABLE users ADD COLUMN is_admin BOOLEAN NOT NULL DEFAULT false;

-- Блокировка, а не удаление: кто и когда имел доступ, должно остаться видно.
ALTER TABLE users ADD COLUMN blocked_at TIMESTAMPTZ;

ALTER TABLE users ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- sessions — сессии сайта.
--
-- Таблица, а не подписанный токен: отзыв обязан быть мгновенным. Блокировка,
-- смена пароля или роли удаляет строки пользователя — следующий запрос с его
-- cookie получает 401.
--
-- Хранится SHA-256 токена, а не сам токен: утечка дампа базы не должна
-- давать готовые cookie.
CREATE TABLE IF NOT EXISTS sessions (
    token_hash   BYTEA PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    user_agent   TEXT
);

CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions (user_id);

-- pnl_lines — строки ОПиУ сверху вниз.
--
-- В базе, а не в MD и не в константах Go: структуру правит финансовый
-- директор из админки. Непротиворечивость (допустимые kind и expand, статья
-- привязана только к разделу) проверяет internal/pnl — и при сохранении,
-- и при каждом расчёте.
--
-- section — сумма проводок по статьям, привязанным в pnl_item_map;
-- total  — нарастающая сумма всех section выше. Формулы у итога нет,
--          поэтому «забыть» раздел, как Gross Profit в листе, он не может.
CREATE TABLE IF NOT EXISTS pnl_lines (
    id     SERIAL PRIMARY KEY,
    code   TEXT UNIQUE NOT NULL,
    title  TEXT NOT NULL,
    kind   TEXT NOT NULL,   -- 'section' | 'total'
    -- Порядок строк. Шаг 10 — чтобы вставить строку, не перенумеровывая
    -- соседние. Граница доступа роли сравнивается по sort, а не по code:
    -- коды лексикографически не упорядочены ('5.1' > '10').
    sort   INT UNIQUE NOT NULL,
    -- Путь раскрытия раздела: '{division,sub_item}' — подразделение → подстатья,
    -- '{}' — раздел одной суммой (у итога всегда).
    expand TEXT[] NOT NULL DEFAULT '{}'
);

-- pnl_item_map — разметка статей.
--
-- line_id указывает раздел; NULL — статья исключена намеренно (движение денег);
-- статья, которой здесь нет вовсе, — неразмеченная: отчёт с ней неполон.
--
-- PRIMARY KEY по статье — «разделы не пересекаются» гарантирует база.
--
-- Имя, а не FK на items: статья попадает в items только с первой проводкой,
-- а разметить её нужно заранее (`сообщества расходы` в листе уже есть, проводок
-- нет), и на свежей базе items пуст до первого прогона парсера. Имя хранится
-- строчными без пробелов по краям — приводит pnl.NormalizeItem.
CREATE TABLE IF NOT EXISTS pnl_item_map (
    item_name TEXT PRIMARY KEY,
    line_id   INT REFERENCES pnl_lines(id)
);

-- Сид структуры — из docs/pnl-structure.md. Как и сид settings в 000002, это
-- вставка справочных строк, а не правка данных: ON CONFLICT DO NOTHING не
-- затирает то, что финдир уже поправил в админке, сколько раз ни накатывай.
INSERT INTO pnl_lines (sort, code, kind, title, expand) VALUES
    ( 10, '1.1', 'section', 'Выручка от основной деятельности',  '{division}'),
    ( 20, '1.2', 'section', 'Возвраты от основной деятельности', '{division}'),
    ( 30, '1.3', 'section', 'Расходы от основной деятельности',  '{division,sub_item}'),
    ( 40, '1',   'total',   'Валовая прибыль',                   '{}'),
    ( 50, '2',   'section', 'Административные расходы',          '{sub_item}'),
    ( 60, '3',   'section', 'Общие налоги',                      '{sub_item}'),
    ( 70, '4',   'total',   'Операционная прибыль',              '{}'),
    ( 80, '5.1', 'section', 'Прочие расходы',                    '{division,sub_item}'),
    ( 90, '5.2', 'section', 'Прочие доходы',                     '{division,sub_item}'),
    (100, '5.3', 'section', 'Возвраты прочих доходов',           '{division,sub_item}'),
    (110, '6',   'total',   'Чистая прибыль',                    '{}'),
    (120, '7.1', 'section', 'Дивиденды',                         '{sub_item}'),
    (130, '7.2', 'section', 'Доходы от инвестиций',              '{sub_item}'),
    (140, '7.3', 'section', 'Вклады в инвестиции',               '{sub_item}'),
    (150, '7.4', 'section', 'Сообщества доходы',                 '{sub_item}'),
    (160, '7.5', 'section', 'Сообщества расходы',                '{sub_item}'),
    (170, '8',   'total',   'Нераспределённая прибыль',          '{}')
ON CONFLICT DO NOTHING;

-- Левый джойн: исключённые статьи (code NULL) получают line_id NULL.
INSERT INTO pnl_item_map (item_name, line_id)
SELECT m.item_name, l.id
FROM (VALUES
    ('доходы',                 '1.1'),
    ('возврат доходов',        '1.2'),
    ('произв. расходы',        '1.3'),
    ('админ. расходы',         '2'),
    ('общие налоги',           '3'),
    ('прочие расходы',         '5.1'),
    ('прочие доходы',          '5.2'),
    ('возврат прочих доходов', '5.3'),
    ('дивиденды',              '7.1'),
    ('инвест. доходы',         '7.2'),
    ('инвест. расходы',        '7.3'),
    ('сообщества доходы',      '7.4'),
    ('сообщества расходы',     '7.5'),
    -- Внутреннее движение денег: в ОПиУ не входит никогда.
    ('перевод',                NULL),
    ('пополнение',             NULL),
    ('перевод на оператора',   NULL),
    ('движение денег',         NULL),
    ('займы',                  NULL)
) AS m (item_name, code)
LEFT JOIN pnl_lines l ON l.code = m.code
ON CONFLICT DO NOTHING;

COMMENT ON COLUMN sub_items.item_id IS
    'Справочное: от последнего апсерта. Одна подстатья встречается под несколькими '
    'статьями, поэтому пары статья → подстатья брать из data, а не отсюда';
