# Готовые запросы

Ad-hoc SQL для разбора данных руками (`psql`), не часть кода сервисов.

Все запросы читают вью `data_current` — текущий срез. По произвольной версии
подставляется `data` с явным `WHERE snapshot_id = $1`: без фильтра по версии
запрос вернёт все срезы разом и задвоит суммы, см.
[data-versioning.md](data-versioning.md).

## Строки без обязательной аналитики

Подразделение, статья и период — разрезы, по которым строится отчёт: строка без
любого из них лежит в базе, но в отчёте невидима. Это те же строки, что парсер
считает в `parser.Report.Gaps` и упоминает в сообщении администратору; запрос
показывает их поимённо, чтобы было что чинить в Google Sheets.

```sql
SELECT d.id,
       d.date,
       d.bank,
       d.organization,
       d.sender,
       d.description,
       COALESCE(d.debet, d.credit) AS amount,
       dv.name AS division,
       it.name AS item,
       d.period,
       concat_ws(', ',
           CASE WHEN d.division_id IS NULL THEN 'подразделение' END,
           CASE WHEN d.item_id     IS NULL THEN 'статья'        END,
           CASE WHEN d.period      IS NULL THEN 'период'        END
       ) AS missing
FROM data_current d
LEFT JOIN divisions dv ON dv.id = d.division_id
LEFT JOIN items     it ON it.id = d.item_id
WHERE d.division_id IS NULL
   OR d.item_id     IS NULL
   OR d.period      IS NULL
ORDER BY d.date, d.id;
```

Колонка `missing` перечисляет недостающие поля строки: одна строка обычно пуста
сразу по нескольким, поэтому число строк здесь меньше суммы трёх счётчиков.

`division_id`, `item_id` и `period` — nullable, пустая ячейка листа доезжает как
`NULL`, а не как пустая строка, поэтому `IS NULL` достаточно и проверять `= ''`
не нужно. Имена справочников подтягиваются `LEFT JOIN`: `INNER` выбросил бы ровно
те строки, ради которых запрос и написан.

## Доступ к боту

Бот отвечает только тем, кто есть в `users`; администратор из `TELEGRAM_ADMIN_ID`
пускается всегда и в таблице быть не обязан — иначе сразу после миграции выдать
доступ первому пользователю было бы некому.

Telegram-id узнаётся из логов бота: отказ пишется как `access denied user=<id>`,
то есть достаточно попросить человека отправить боту любую команду.

Таблица `users` общая с сайтом (миграция `000003`), поэтому у записи обязательна
роль — она задаёт, до какой строки ОПиУ человек видит отчёт: `division_head`
(руководитель отдела, нужен `division_id`), `coo`, `cco`, `founder`, `cfo`.
Отзыв — блокировкой, а не удалением: кто и когда имел доступ, остаётся видно,
и блокировка закрывает и бота, и сайт.

```sql
-- выдать доступ
INSERT INTO users (telegram_id, username, role)
VALUES (123456789, 'almas', 'cfo')
ON CONFLICT (telegram_id) DO UPDATE
SET username = EXCLUDED.username, role = EXCLUDED.role, blocked_at = NULL, updated_at = now();

-- руководителю отдела — ещё и подразделение
INSERT INTO users (telegram_id, username, role, division_id)
VALUES (123456789, 'almas', 'division_head', (SELECT id FROM divisions WHERE name = 'отдел продаж'));

-- отозвать (сессии сайта гасятся вместе с доступом)
UPDATE users SET blocked_at = now(), updated_at = now() WHERE telegram_id = 123456789;
DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE telegram_id = 123456789);

-- кто имеет доступ
SELECT id, telegram_id, login, username, role, division_id, is_admin, blocked_at, created_at
FROM users ORDER BY created_at;
```

## Структура ОПиУ

Строки отчёта — `pnl_lines`, разметка статей — `pnl_item_map` (`line_id NULL` —
статья исключена намеренно). Статья, которой нет в разметке, в отчёт не входит
и показывается администратору как неразмеченная.

```sql
-- неразмеченные статьи рабочего среза
SELECT DISTINCT it.name
FROM data d JOIN items it ON it.id = d.item_id
WHERE d.snapshot_id = (SELECT max(id) FROM snapshots)
  AND lower(btrim(it.name)) NOT IN (SELECT item_name FROM pnl_item_map);

-- разметить статью (имя — строчными, без пробелов по краям)
INSERT INTO pnl_item_map (item_name, line_id)
VALUES ('новая статья', (SELECT id FROM pnl_lines WHERE code = '5.1'));
```

## Настройки отчётов

Статьи, исключаемые из `/closed_reports`, лежат в `settings` под ключом
`closed_reports`. Это внутреннее движение денег: без отсечения каждая такая
проводка попадает в сводку второй половиной той же суммы и задваивает итоги.

Сравнение в отчёте идёт без учёта регистра, так что писать можно строчными.

```sql
-- посмотреть
SELECT jsonb_pretty(value::jsonb) FROM settings WHERE key = 'closed_reports';

-- заменить список целиком
UPDATE settings
SET value = '{"excluded_items": ["пополнение", "перевод", "движение денег"]}',
    updated_at = now()
WHERE key = 'closed_reports';
```

Тип колонки — `json`, а не `jsonb`: значение правится человеком, и `jsonb`
переставлял бы ключи и терял отступы при каждом сохранении. Читается оно целиком
по первичному ключу, поэтому операторы и индексы `jsonb` здесь не нужны;
`jsonb_pretty` выше — разовое приведение ради читаемого вывода в psql.

Отсутствие строки `closed_reports` — ошибка, а не «пустой список исключений»:
бот откажется считать отчёт и попросит накатить миграции. Молча посчитанная
сводка с переводами внутри выглядит исправной, и расхождение всплыло бы нескоро.

## Настройки сайта

Живут в `settings` (миграция `000004`) под двумя ключами. Читаются на каждом
использовании (вход, продление сессии, заведение пароля, запрос отчёта), поэтому
**перезапуск не нужен**: новое значение действует со следующего обращения. Уже
выданные сессии держатся до своего `expires_at`, пока их не продлят.

| Ключ   | Поле                   | Сид | Что значит                                                    |
| ------ | ---------------------- | --: | ------------------------------------------------------------- |
| `auth` | `session_ttl_days`     |  90 | скользящий срок сессии, дней                                  |
| `auth` | `login_max_failures`   |   5 | неудачных входов подряд с пары «логин + адрес» до запрета     |
| `auth` | `login_window_minutes` |  15 | окно подсчёта неудач и срок запрета, минут                    |
| `auth` | `min_password_length`  |  10 | минимальная длина пароля при заведении и смене                |
| `pnl`  | `default_months`       |   6 | месяцев в отчёте, когда колонки не выбраны                    |
| `pnl`  | `max_columns`          |  12 | потолок колонок отчёта (его же получает фронт в `max_columns`) |

```sql
-- посмотреть
SELECT key, value FROM settings WHERE key IN ('auth', 'pnl');

-- поменять (значение целиком, все поля)
UPDATE settings
SET value = '{"session_ttl_days": 30, "login_max_failures": 5, "login_window_minutes": 15, "min_password_length": 12}',
    updated_at = now()
WHERE key = 'auth';

UPDATE settings
SET value = '{"default_months": 6, "max_columns": 18}', updated_at = now()
WHERE key = 'pnl';
```

**Если строки нет или поле пустое/меньше 1**, код берёт значение по умолчанию и пишет
в лог предупреждение (`нет настройки…` / `поле настройки пусто или меньше 1…`):
настройка не должна ронять вход и отчёт. Умолчания в коде: срок сессии — **1 день**
(самый короткий: ошибка настройки не должна делать сессию долгой), остальные — как в
сиде. Число месяцев по умолчанию не больше потолка колонок. Сломанный JSON, в отличие
от отсутствия строки, — ошибка: его надо чинить. Верхних границ у значений нет.
