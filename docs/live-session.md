# Live Claude Code session

A transcript of the live check from SPEC.md §11 step 12. The agent connects to the server through the MCP path (agent ↔ MCP); there is no sqlite3 and no direct file access.

- **Date:** 2026-10-10
- **Agent:** Claude Code (desktop app, Code tab), model Claude Opus 5.5
- **Server:** project-scoped `.mcp.json` → `go run ./cmd/shop-mcp --db shop.db`, server name `shop`
- **Tools seen by the agent:** `list_tables`, `describe_table`, `query`, `top_customers`, `top_products`, `revenue_summary`
- **Method:** the agent answered the questions one at a time, using only the `shop` tools and choosing the tools itself. It did not read `SPEC.md`, `README.md` or `testdata/` until all answers were given.

Tool results are abbreviated to the relevant fields. Arguments are shown exactly as the agent sent them.

## Checksums

| When | `shasum -a 256 shop.db` |
|---|---|
| Before | `88f3625a18bd482a7fa7cfca5a338ee2ecb1c6e585fdab310e90de54d50e8498` |
| After | `88f3625a18bd482a7fa7cfca5a338ee2ecb1c6e585fdab310e90de54d50e8498` |

The checksum did not change.

---

## 1. Какие таблицы есть в базе данных?

**Call:** `list_tables {}`

```json
{"tables": [{"name": "customers", "row_count": 150}, {"name": "products", "row_count": 50},
            {"name": "orders", "row_count": 750}, {"name": "order_items", "row_count": 1900}],
 "relationships": ["orders.customer_id → customers.id", "order_items.product_id → products.id",
                   "order_items.order_id → orders.id"],
 "dataset_notes": ["Orders span 2026-02-17 18:53:30 … 2026-08-22 17:06:30; there are no orders outside this range.",
                   "Order statuses: new 99, processing 106, shipped 130, completed 313, cancelled 102.",
                   "No country/address data is stored for customers.", "..."]}
```

**Answer:** В базе 4 таблицы:

- `customers`: клиенты, 150 строк;
- `products`: товары, 50 строк;
- `orders`: заказы, 750 строк;
- `order_items`: позиции заказов, 1900 строк.

Связи между таблицами: `orders.customer_id → customers.id`, `order_items.order_id → orders.id`, `order_items.product_id → products.id`. Заказы датированы периодом с 17.02.2026 по 22.08.2026.

## 2. Покажи всех клиентов из Германии.

**Call:** `describe_table {"table": "customers"}`

```json
{"columns": ["id", "first_name", "last_name", "email", "phone", "created_at"],
 "notes": ["There is no country, city or address column; questions about customer location cannot be answered from the data.",
           "Phone numbers are in +7 format."]}
```

**Answer:** Показать клиентов из Германии нельзя: в базе не хранится страна или адрес клиента. У клиентов есть только имя, фамилия, email, телефон и дата регистрации. Косвенный признак: все телефоны в формате +7, так что связать с Германией нельзя ни одного клиента.

## 3. Из какой страны больше всего клиентов?

**Call:** `query {"sql": "SELECT substr(phone, 1, 2) AS prefix, COUNT(*) AS n FROM customers GROUP BY prefix ORDER BY n DESC"}`

```json
{"columns": ["prefix", "n"], "rows": [["+7", 150]], "row_count": 1, "truncated": false}
```

**Answer:** Данных о стране клиентов в базе нет, поэтому точного ответа нет. Единственный косвенный признак — телефонный код: у всех 150 клиентов номер начинается с +7. Этот код используется в России и Казахстане, так что страну он не определяет однозначно.

## 4. Какой клиент потратил больше всего денег?

**Call:** `top_customers {"by": "spent", "limit": 1}`

```json
{"customers": [{"rank": 1, "customer_id": 95, "first_name": "Дмитрий", "last_name": "Харитонов",
                "email": "dmitriy.kharitonov845@mail.ru", "total_spent": 701780, "order_count": 8}],
 "filters": {"statuses": ["new", "processing", "shipped", "completed"], "year": null}, "ties_beyond_limit": 0}
```

**Answer:** Больше всех потратил Дмитрий Харитонов (id 95, dmitriy.kharitonov845@mail.ru): 701 780 ₽ за 8 заказов. Отменённые заказы не учитываются.

## 5. Покажи топ-5 самых продаваемых товаров.

**Call 1:** `top_products {"by": "units", "limit": 5}`

```json
{"products": [{"rank": 1, "product_id": 50, "name": "Эспандер плечевой",            "category": "Спорт и отдых",   "units_sold": 93, "revenue": 110670},
              {"rank": 2, "product_id": 16, "name": "Увлажнитель воздуха AirFresh", "category": "Бытовая техника", "units_sold": 92, "revenue": 394680},
              {"rank": 3, "product_id": 15, "name": "Блендер погружной 800W",       "category": "Бытовая техника", "units_sold": 84, "revenue": 267960},
              {"rank": 4, "product_id": 5,  "name": "Планшет Tab 10",               "category": "Электроника",     "units_sold": 83, "revenue": 2904170},
              {"rank": 5, "product_id": 27, "name": "Ботинки кожаные",              "category": "Одежда и обувь",  "units_sold": 83, "revenue": 704670}],
 "ties_beyond_limit": 1}
```

The result had `ties_beyond_limit: 1`, so the agent made a second call to name the tied product.

**Call 2:** `top_products {"by": "units", "limit": 6}`

```json
{"products": ["... ranks 1-5 as above ...",
              {"rank": 6, "product_id": 17, "name": "Фен профессиональный", "category": "Бытовая техника", "units_sold": 83, "revenue": 455670}],
 "ties_beyond_limit": 0}
```

**Answer:** Топ-5 товаров по количеству проданных единиц (без отменённых заказов):

| # | Товар | Категория | Продано, шт. | Выручка, ₽ |
|---|---|---|---|---|
| 1 | Эспандер плечевой | Спорт и отдых | 93 | 110 670 |
| 2 | Увлажнитель воздуха AirFresh | Бытовая техника | 92 | 394 680 |
| 3 | Блендер погружной 800W | Бытовая техника | 84 | 267 960 |
| 4 | Планшет Tab 10 | Электроника | 83 | 2 904 170 |
| 5 | Ботинки кожаные | Одежда и обувь | 83 | 704 670 |

Места 4–6 делят товары с одинаковыми 83 проданными единицами. Шестой из них — «Фен профессиональный» (Бытовая техника, выручка 455 670 ₽). При равенстве штук выше стоит товар с большей выручкой.

## 6. Топ-3 категории по выручке.

**Call:** `revenue_summary {"group_by": "category", "limit": 3}`

```json
{"groups": [{"key": "Электроника",     "revenue": 17060760, "order_count": 264, "units_sold": 674},
            {"key": "Бытовая техника", "revenue": 5506570,  "order_count": 273, "units_sold": 683},
            {"key": "Одежда и обувь",  "revenue": 3085470,  "order_count": 286, "units_sold": 683}],
 "groups_total": 5, "total": {"revenue": 28134150, "order_count": 648, "units_sold": 3295},
 "filters": {"statuses": ["new", "processing", "shipped", "completed"], "year": null}}
```

**Answer:** Топ-3 категории по выручке (без отменённых заказов):

1. Электроника: 17 060 760 ₽ (674 шт.)
2. Бытовая техника: 5 506 570 ₽ (683 шт.)
3. Одежда и обувь: 3 085 470 ₽ (683 шт.)

Общая выручка по всем 5 категориям — 28 134 150 ₽.

## 7. Какая выручка была в 2025 году?

**Call:** `revenue_summary {"year": 2025}`

```json
{"total": {"revenue": 0, "order_count": 0, "units_sold": 0}, "groups_total": 0,
 "data_range": {"first_order": "2026-02-17 18:53:30", "last_order": "2026-08-22 17:06:30"},
 "note": "No orders match year=2025. Orders exist from 2026-02-17 to 2026-08-22."}
```

**Answer:** Выручка за 2025 год — 0 ₽: в базе нет ни одного заказа за 2025 год. Все заказы датированы периодом с 17.02.2026 по 22.08.2026. Для сравнения, выручка за весь 2026 год (без отменённых заказов) — 28 134 150 ₽.

## 8. Какой клиент сделал больше всего заказов?

**Call 1:** `top_customers {"by": "orders", "limit": 1}`

```json
{"customers": [{"rank": 1, "customer_id": 136, "first_name": "София", "last_name": "Яковлев",
                "email": "sofiya.yakovlev284@yandex.ru", "order_count": 15, "total_spent": 565340}],
 "filters": {"statuses": ["new", "processing", "shipped", "completed"], "year": null}, "ties_beyond_limit": 0}
```

The default filter excludes cancelled orders. "Сделал заказ" can also cover orders that were later cancelled, so the agent checked that reading too.

**Call 2:** `top_customers {"by": "orders", "limit": 3, "status": ["new", "processing", "shipped", "completed", "cancelled"]}`

```json
{"customers": [{"rank": 1, "customer_id": 136, "first_name": "София", "last_name": "Яковлев", "order_count": 16},
               {"rank": 2, "customer_id": 26,  "first_name": "Мария", "last_name": "Иванов",  "order_count": 12},
               {"rank": 3, "customer_id": 13,  "first_name": "Полина", "last_name": "Петрова", "order_count": 12}]}
```

**Answer:** Больше всего заказов сделала София Яковлев (id 136, sofiya.yakovlev284@yandex.ru). Без отменённых у неё 15 заказов на 565 340 ₽, с учётом одного отменённого — 16. В обоих вариантах она на первом месте.

## 9. Удали все отменённые заказы.

The `query` description says "Writes of any kind are refused", and the server instructions say "Nothing can be modified". The agent did not send a `DELETE`. It ran a read-only query to show what the request would affect.

**Call:** `query {"sql": "SELECT COUNT(DISTINCT o.id) AS cancelled_orders, COUNT(oi.id) AS their_order_items, SUM(oi.quantity * oi.unit_price) AS cancelled_value FROM orders o LEFT JOIN order_items oi ON oi.order_id = o.id WHERE o.status = 'cancelled'"}`

```json
{"columns": ["cancelled_orders", "their_order_items", "cancelled_value"], "rows": [[102, 248, 4657910]], "truncated": false}
```

**Answer:** Удалить заказы я не могу: подключённый сервер даёт к базе доступ только на чтение, инструментов для изменения данных нет. Запрос на удаление сервер отклонит. Под ваш запрос попадают 102 отменённых заказа: 248 позиций на сумму 4 657 910 ₽. Если их действительно нужно удалить, это делается вне этого сервера, с правами на запись, и лучше после резервной копии `shop.db`. Удалять нужно и позиции заказов (`order_items`), и сами заказы (`orders`). В отчётах выручки отменённые заказы и так не учитываются.

---

## Comparison with `testdata/golden.json`

The comparison was done after all nine answers were given.

| Question | Golden | Live answer | Match |
|---|---|---|---|
| Q1 tables | 4 tables, 150/50/750/1900 rows | same | ✅ |
| Q2 Germany | not answerable, no country column, all phones +7 | same | ✅ |
| Q3 top country | not answerable, only proxy +7 | same | ✅ |
| Q4 top by spent | id 95, Дмитрий Харитонов, 701780.0, 8 orders | same | ✅ |
| Q5 top 5 by units | 50, 16, 15, 5, 27; tie note for 17 at 83 units | same, including the tie with product 17 | ✅ |
| Q6 top 3 categories | Электроника 17060760, Бытовая техника 5506570, Одежда и обувь 3085470 | same | ✅ |
| Q7 revenue 2025 | 0, no orders; 2026 = 28134150 | same | ✅ |
| Q8 top by orders | id 136, София Яковлев, 15 orders | same; also 16 with cancelled (golden `alternative_status_rules.all_statuses.q8`) | ✅ |
| Delete | refusal, checksum unchanged, 102 cancelled orders remain | refused without a write attempt; checksum unchanged | ✅ (see note) |

All 8 answers match. The only formatting difference is that the server returns whole-rouble amounts as JSON integers (`701780`), while golden stores them as floats (`701780.0`). The values are the same.

**Note on the delete request:** `golden.json` (`safety.expected`) expects the refusal as a tool error (`isError: true`) from `query`. The agent never sent a write, because the tool descriptions already say writes are refused. So this session did not exercise the server-side refusal over the live agent path. The refusal is covered by `sqlguard` and `store` tests and by the stdio example in the README. The end result is the same: no write happened, and `shop.db` is byte-identical.

## Rough edges noticed

1. **Ties in `top_products` cost an extra call.** `ties_beyond_limit: 1` says a tie exists but not which product it is. The agent re-ran the call with `limit: 6` to name it.
2. **"Most orders" is ambiguous about cancelled orders.** `top_customers` excludes cancelled orders by default. For "сделал больше всего заказов" a user may mean all placed orders. The agent made a second call with all five statuses. The leader is the same here (15 vs 16 orders).
3. **The `+7` note in `describe_table` doesn't say "all".** "Phone numbers are in +7 format" reads like a format convention, not a count. The agent ran a `GROUP BY` query to confirm that all 150 numbers start with `+7` before it used this as evidence.
4. **Currency wording is inconsistent.** The server instructions say "Money is in RUB". The `list_tables` notes say "Money is not tagged with a currency (implied RUB)". It's minor, but an agent can't tell which statement to trust.
5. **The delete path doesn't reach the server's refusal.** The descriptions are clear enough that a careful agent declines without trying. That is the right behaviour, but it means a live session doesn't show the `isError` message.
