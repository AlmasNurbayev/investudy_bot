# Источник данных: лист ДДС (Google Sheets → PostgreSQL)

Как колонки листа ложатся в таблицу `data`. Версионирование срезов —
[data-versioning.md](data-versioning.md).

## Колонки

Лист **ДДС** (движение денежных средств) — банковские транзакции. 24 колонки:

| Поле (англ.)   | Русское название    | Тип / формат                                                |
| -------------- | ------------------- | ----------------------------------------------------------- |
| `date`         | Дата                | `DD.MM.YYYY`                                                |
| `numOper`      | #                   | строка                                                      |
| `typeOper`     | Тип                 | строка                                                      |
| `debetVal`     | Дебет валюта        | decimal                                                     |
| `creditVal`    | Кредит валюта       | decimal                                                     |
| `exRate`       | Курс                | decimal                                                     |
| `debet`        | Дебет               | decimal, запятая как разделитель; исключает `credit`        |
| `credit`       | Кредит              | decimal, запятая как разделитель; исключает `debet`         |
| `sender`       | Бенеф-р/отправитель | контрагент + ИИН/БИН                                        |
| `description`  | Назначение платежа  | свободный текст                                             |
| `bank`         | Банк                | короткое имя (`Kaspi`, `Halyk`, …)                          |
| `period`       | Период              | `01.MM.YYYY` — первое число месяца                          |
| `organization` | Организация         | юр. лицо (`Aligee`)                                         |
| `division_id`  | Подразделение       | FK → `divisions(id)`                                        |
| `item_id`      | Статья              | FK → `items(id)`                                            |
| `sub_item_id`  | Подстатья           | FK → `sub_items(id)` (sub_items.item_id → items)            |
| `comment1`     | Учет                |                                                             |
| `comment2`     | Комментарий         |                                                             |
| `fin_type_id`  | Тип                 | FK → `fin_types(id)` (`доход` / `расход` / `возврат` и др.) |
| `sumDash`      | СуммаДаш            | decimal                                                     |
| `vid_id`       | Вид                 | FK → `vids(id)` (центр затрат)                              |
| `sumRevenue`   | СуммаДоход          | decimal; заполнен только для `type=доход`                   |
| `sumCost`      | СуммаРасход         | decimal; заполнен только для `type=расход`                  |
| `sumReturn`    | СуммаВозврат        | decimal; заполнен только для `type=возврат`                 |

## Справочники

**Справочные таблицы:** `divisions`, `items`, `sub_items`, `fin_types`, `vids` — все FK nullable. Парсер апсертит значение по имени и получает id перед вставкой в `data`. Для `sub_items` сначала upsert родительского `item`, затем `sub_item` с `item_id`.

Апсерт пишется как `INSERT … ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id`, а **не** через `DO NOTHING RETURNING id`: последний на конфликте не возвращает ни одной строки, то есть для уже существующего значения id не придёт. Строк в справочниках десятки, лишняя запись роли не играет.

Особенности разбора чисел и дат — [parser.md](parser.md).
