# shop-mcp

An [MCP](https://modelcontextprotocol.io) server in Go that gives an AI agent **read-only** access to the SQLite database of an online shop (`shop.db`). It speaks MCP over stdio and ships as a single static binary.

Why a dedicated server instead of handing the agent a SQL shell:

- **It cannot write.** Three independent layers stop any modification (see [Security model](#security-model)).
- **It answers common questions in one call.** `top_customers`, `top_products` and `revenue_summary` encode the revenue rule once, so the agent doesn't have to rediscover it (and get it wrong) on every question.
- **It tells the agent what the data cannot answer.** There is no country column and no 2025 orders; tools say so explicitly instead of returning something the agent might misread.
- **Errors are hints.** A wrong column name returns the list of real columns; a `DELETE` returns an explanation and the equivalent `SELECT`.

## Install

Requires Go 1.27+.

```bash
git clone https://github.com/lama2x2/shop-mcp && cd shop-mcp
```

```bash
make build
```

This produces `bin/shop-mcp` (pure Go, `CGO_ENABLED=0`). Without make: `go build -o bin/shop-mcp ./cmd/shop-mcp`.

## Configure

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `--db` | `SHOP_DB_PATH` | `./shop.db`, then `shop.db` next to the executable | Database file. The flag wins over the env var. |
| `--query-timeout` | | `5s` | Time limit for each tool call. |
| `--log-level` | | `info` | `debug` (also logs SQL text), `info` (one line per tool call), `warn`, `error`. |
| `--version` | | | Print the version and exit. |

Logs go to **stderr**; stdout carries only the MCP protocol. If the database can't be found the server prints one line and exits with code 1:

```text
shop-mcp: database not found at "shop.db" (set --db or SHOP_DB_PATH)
```

## Run

```bash
./bin/shop-mcp --db ./shop.db
```

The server waits for an MCP client on stdin. It exits with code 0 when the client disconnects or on SIGINT/SIGTERM.

## Connect

Use absolute paths in all client configs. Ready-made files are in [`examples/`](examples).

**Claude Code**

```bash
claude mcp add shop -- /abs/path/shop-mcp/bin/shop-mcp --db /abs/path/shop-mcp/shop.db
```

or put [`examples/claude-code.mcp.json`](examples/claude-code.mcp.json) into the project as `.mcp.json` ([`go run` variant](examples/claude-code-go-run.mcp.json)).

**Claude Desktop**: merge [`examples/claude_desktop_config.json`](examples/claude_desktop_config.json) into `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) and restart the app.

**Cursor**: merge [`examples/cursor-mcp.json`](examples/cursor-mcp.json) into `~/.cursor/mcp.json` or `.cursor/mcp.json` in a project.

**Docker** (the image contains the binary and `shop.db`):

```bash
docker build -t shop-mcp .
```

```bash
docker run -i --rm shop-mcp
```

Use `docker` as the command and `["run", "-i", "--rm", "shop-mcp"]` as args in any client ([example](examples/docker.mcp.json)).

## Tools

All tools are annotated `readOnlyHint: true`, `idempotentHint: true`, `openWorldHint: false`. Analytics responses echo the applied `filters` so the agent can state which rule it used.

| Tool | Use it for | Input | Returns |
|---|---|---|---|
| `list_tables` | Discovering the database | — | tables with row counts and descriptions, foreign-key relationships, dataset notes (date range, status counts, categories, revenue rule, missing data) |
| `describe_table` | Before writing SQL | `table` | columns, foreign keys, incoming references, 3 sample rows, notes |
| `query` | Anything not covered below | `sql` (one `SELECT` / `WITH … SELECT`, ≤ 10 000 chars), `limit` (1–500, default 100), `offset` | `columns`, `rows`, `row_count`, `truncated`, `next_offset` |
| `top_customers` | "Who spent the most / ordered most" | `by` = `spent`\|`orders`, `year`, `status[]`, `limit` (1–50, default 5) | ranked customers with `total_spent`, `order_count`; `ties_beyond_limit` |
| `top_products` | "Best sellers", "top by revenue" | `by` = `units`\|`revenue`, `year`, `status[]`, `category`, `limit` (1–50, default 5) | ranked products with `units_sold`, `revenue`; `ties_beyond_limit` |
| `revenue_summary` | Revenue totals and splits | `group_by` = `none`\|`year`\|`month`\|`category`\|`status`, `year`, `status[]`, `limit` (1–100, default 50) | `total`, `groups`, `groups_total`, `data_range`, `note` |

Ties are broken deterministically (primary metric → secondary metric → id). When the row right after the limit ties with the last row returned, `ties_beyond_limit` says how many more rows tie, so the agent can mention it.

## Security model

Each layer stops a write on its own, and each has its own tests.

1. **Read-only connection.** The DSN is `file:<path>?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(2000)`. Even if every check above it were bypassed, SQLite refuses with `attempt to write a readonly database`. This is the layer that actually guarantees safety; the others exist for good error messages and as defense in depth.
2. **SQL guard** (`internal/sqlguard`). A small lexer that understands strings, quoted identifiers and comments. It accepts exactly one `SELECT` or `WITH … SELECT` statement and rejects any bare write/DDL/transaction keyword (`INSERT`, `UPDATE`, `DELETE`, `REPLACE`, `DROP`, `ATTACH`, `PRAGMA`, …) anywhere, including inside CTEs, plus `load_extension`. Literals are opaque, so `WHERE note = 'DROP TABLE'` and `SELECT 1 AS "delete"` pass. The guard runs inside `store.Query`, so there's no path to execute unchecked SQL.
3. **Resource limits.** Every tool call runs under a timeout (default 5 s); the driver interrupts the running statement on cancellation. `query` returns at most 500 rows per call, and SQL text is capped at 10 000 characters.

The analytics tools use only static SQL with `?` placeholders; enum values and table names are checked against allow-lists before use.

## Revenue rule and data caveats

- **Revenue** = `SUM(order_items.quantity * order_items.unit_price)` over orders whose status is `new`, `processing`, `shipped` or `completed`, so **cancelled orders are excluded** by default. Pass `status` to change this (e.g. `["completed"]` for realized revenue only, or all five statuses to include cancelled). `products.price` is the current list price and is never used for revenue. Amounts are rounded to 2 decimals; currency isn't stored (implied RUB).
- **No country data.** `customers` has no country or address column (all phone numbers are `+7`). Questions such as "customers from Germany" can't be answered from the data, and the tools say so.
- **No 2025 orders.** All orders are dated 2026-02-17 … 2026-08-22. `revenue_summary {year: 2025}` returns 0 with a `note`, not an error.
- Full names aren't unique (there are two "Иван Соловьев"), so customers are identified by `id` and `email`.

## Example session

The responses below are real output from `bin/shop-mcp` against the bundled `shop.db`, taken over stdio. Each question needs exactly one tool call. Output is abbreviated to the relevant fields.

**1. Which tables are in the database?** → `list_tables`

```json
{"tables": [{"name": "customers", "row_count": 150}, {"name": "products", "row_count": 50},
            {"name": "orders", "row_count": 750}, {"name": "order_items", "row_count": 1900}],
 "dataset_notes": ["Orders span 2026-02-17 18:53:30 … 2026-08-22 17:06:30; there are no orders outside this range.",
                   "No country/address data is stored for customers.", "..."]}
```

**2. Which customers are from Germany?** / **3. Which country has the most customers?** → `describe_table {"table": "customers"}`

```json
{"columns": ["id", "first_name", "last_name", "email", "phone", "created_at"],
 "notes": ["There is no country, city or address column; questions about customer location cannot be answered from the data.",
           "Phone numbers are in +7 format."]}
```

The expected answer: country isn't stored, so neither question can be answered from the data (all phone numbers use +7).

**4. Who spent the most?** → `top_customers {"by": "spent", "limit": 1}`

```json
{"customers": [{"rank": 1, "customer_id": 95, "first_name": "Дмитрий", "last_name": "Харитонов",
                "email": "dmitriy.kharitonov845@mail.ru", "total_spent": 701780, "order_count": 8}],
 "filters": {"statuses": ["new", "processing", "shipped", "completed"], "year": null}, "ties_beyond_limit": 0}
```

**5. Top 5 products by units sold** → `top_products {"by": "units", "limit": 5}`

```json
{"products": [{"rank": 1, "product_id": 50, "name": "Эспандер плечевой",            "units_sold": 93, "revenue": 110670},
              {"rank": 2, "product_id": 16, "name": "Увлажнитель воздуха AirFresh", "units_sold": 92, "revenue": 394680},
              {"rank": 3, "product_id": 15, "name": "Блендер погружной 800W",       "units_sold": 84, "revenue": 267960},
              {"rank": 4, "product_id": 5,  "name": "Планшет Tab 10",               "units_sold": 83, "revenue": 2904170},
              {"rank": 5, "product_id": 27, "name": "Ботинки кожаные",              "units_sold": 83, "revenue": 704670}],
 "ties_beyond_limit": 1}
```

`ties_beyond_limit: 1` means a sixth product (Фен профессиональный, also 83 units) ties with ranks 4–5.

**6. Top 3 categories by revenue** → `revenue_summary {"group_by": "category", "limit": 3}`

```json
{"groups": [{"key": "Электроника",     "revenue": 17060760, "order_count": 264, "units_sold": 674},
            {"key": "Бытовая техника", "revenue": 5506570,  "order_count": 273, "units_sold": 683},
            {"key": "Одежда и обувь",  "revenue": 3085470,  "order_count": 286, "units_sold": 683}],
 "groups_total": 5, "total": {"revenue": 28134150, "order_count": 648, "units_sold": 3295}}
```

**7. Revenue in 2025** → `revenue_summary {"year": 2025}`

```json
{"total": {"revenue": 0, "order_count": 0, "units_sold": 0},
 "note": "No orders match year=2025. Orders exist from 2026-02-17 to 2026-08-22."}
```

**8. Which customer placed the most orders?** → `top_customers {"by": "orders", "limit": 1}`

```json
{"customers": [{"rank": 1, "customer_id": 136, "first_name": "София", "last_name": "Яковлев",
                "email": "sofiya.yakovlev284@yandex.ru", "order_count": 15, "total_spent": 565340}]}
```

**Delete all cancelled orders** → the agent has no write tool. If it tries `query {"sql": "DELETE FROM orders WHERE status = 'cancelled'"}`, it gets a tool error (`isError: true`):

```text
Refused: this server provides read-only access. DELETE statements are not allowed; only a single SELECT or WITH … SELECT query can be run. To see the affected rows use: SELECT * FROM orders WHERE status = 'cancelled'.
```

The SHA-256 of `shop.db` was `88f3625a…e8498` both before and after the session.

## Development

```bash
make test
```

```bash
make lint
```

- `make test` runs `go test -race ./...`: guard allow/deny tables, store tests against a temp copy of `shop.db` (read-only connection without the guard, pagination, timeout), and in-memory MCP integration tests that check every question above against [`testdata/golden.json`](testdata/golden.json), the delete refusal (DB checksum unchanged) and error hints.
- `make golden` re-runs [`testdata/golden.sql`](testdata/golden.sql) with `sqlite3 -readonly` so you can regenerate the reference answers deliberately.
- The design and its rationale are in [`SPEC.md`](SPEC.md); conventions for coding agents are in [`CLAUDE.md`](CLAUDE.md).

Layout: `cmd/shop-mcp` (entry point), `internal/sqlguard` (lexer + check), `internal/store` (read-only SQLite access), `internal/tools` (MCP tools and error messages).
