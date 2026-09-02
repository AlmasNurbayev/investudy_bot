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

```sql
-- выдать доступ
INSERT INTO users (telegram_id, username)
VALUES (123456789, 'almas')
ON CONFLICT (telegram_id) DO UPDATE SET username = EXCLUDED.username;

-- отозвать
DELETE FROM users WHERE telegram_id = 123456789;

-- кто имеет доступ
SELECT telegram_id, username, role, created_at FROM users ORDER BY created_at;
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
