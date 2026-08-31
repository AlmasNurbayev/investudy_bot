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
