# shop-mcp — Specification

MCP server in Go that gives an AI agent **read-only** access to the SQLite database of an online shop (`shop.db`). Transport: stdio. Single static binary.

Status: **draft, awaiting approval**. Reference answers: `testdata/golden.json` (queries in `testdata/golden.sql`).

## 1. Stack

| Concern | Choice |
|---|---|
| Language | Go 1.27 (`go 1.27` in `go.mod`), module `github.com/lama2x2/shop-mcp` |
| MCP | `github.com/modelcontextprotocol/go-sdk` v1.8.0, `mcp.StdioTransport` |
| SQLite | `modernc.org/sqlite` v1.60.x (pure Go, `CGO_ENABLED=0`) |
| Logging | `log/slog` → **stderr only** (stdout is the protocol) |
| Other deps | none beyond the two above (stdlib only) |

SDK facts the design relies on (verified in v1.8.0 source):
- `mcp.AddTool[In, Out]` derives input/output JSON Schema from struct tags (`json`, `jsonschema:"<description>"`), validates input before the handler, fills `StructuredContent` from `Out` and mirrors it as JSON text in `Content`.
- A plain `error` returned from a typed handler becomes `CallToolResult{IsError: true}` with the error text — a **tool error**, not a protocol error. This is the only error channel we use.
- `mcp.NewInMemoryTransports()` is used in integration tests.
- `mcp.ServerOptions.Instructions` is sent to the client on initialize.

## 2. Data model (facts from `shop.db`)

```
customers(id PK, first_name, last_name, email UNIQUE, phone, created_at)
  └─< orders(id PK, customer_id FK→customers, order_date, status, total_amount)
        └─< order_items(id PK, order_id FK→orders, product_id FK→products, quantity>0, unit_price)
products(id PK, name, category, price, stock_quantity, created_at) ─< order_items
```

- Row counts: customers 150, products 50, orders 750, order_items 1900.
- `orders.status` ∈ `new, processing, shipped, completed, cancelled` (CHECK constraint).
- Dates are `TEXT` `YYYY-MM-DD HH:MM:SS`. All orders are in **2026-02-17 … 2026-08-22**; there are **no 2025 orders**.
- `order_items.unit_price` = price at purchase time. `orders.total_amount` = Σ items of that order (0 mismatches). `products.price` is the *current* list price — never used for revenue.
- 5 categories (Russian names), 10 products each. Names and categories are in Russian.
- **There is no country / address column.** All 150 phones start with `+7`. Questions about countries cannot be answered from data; tools must make this explicit instead of letting the agent guess.
- Full names are not unique (two "Иван Соловьев"); identify customers by `id` + `email`.

## 3. Business rules

**Revenue** = `SUM(order_items.quantity * order_items.unit_price)` over orders whose status is in the *status filter*.
- Default status filter: `new, processing, shipped, completed` (i.e. **cancelled excluded**).
- The agent may pass an explicit `status` list (e.g. `["completed"]` for realized revenue only, or all five to include cancelled).
- Money is returned as JSON numbers rounded to 2 decimals (`ROUND(x, 2)`), currency is not stored (implied RUB).
- `year` filter applies to `orders.order_date` as a half-open range `>= 'YYYY-01-01' AND < 'YYYY+1-01-01'`.
- Every analytics response echoes the applied rule in `filters` (`statuses`, `year`, …) so the agent can state it.

**Ranking tie-breaks** are deterministic: primary metric DESC → secondary metric DESC → `id` ASC. If the first excluded row has the same primary metric as the last returned row, the response sets `ties_beyond_limit` (count of such rows) so the agent can mention the tie.

## 4. Tools

Common to all tools: `Annotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: false}`; typed `In`/`Out` structs; descriptions written for an LLM (when to use, parameter examples, what is returned, how revenue is computed, limits). Enum parameters are declared as JSON Schema enums. Field names below are the JSON names.

### 4.1 `list_tables`
Use first to discover the database. No input.

Out:
```json
{
  "tables": [{"name": "orders", "row_count": 750, "description": "One row per order: customer_id, order_date, status (new|processing|shipped|completed|cancelled), total_amount = sum of its items."}],
  "relationships": ["orders.customer_id → customers.id", "order_items.order_id → orders.id", "order_items.product_id → products.id"],
  "dataset_notes": ["Orders span 2026-02-17 … 2026-08-22", "No country/address data is stored for customers", "Revenue rule: …"]
}
```
- Tables come from `sqlite_master` (excluding `sqlite_%`); row counts via `COUNT(*)`; descriptions from a static map in `internal/tools` (unknown tables get `""`).
- Order date range and status list are computed at call time, not hard-coded.

### 4.2 `describe_table`
Use before writing SQL for a table. In: `table` (string, required; must be an existing table — otherwise tool error listing available tables).

Out: `name`, `description`, `row_count`, `columns[] {name, type, not_null, primary_key, default}`, `foreign_keys[] {column, references_table, references_column, on_delete}`, `referenced_by[] {table, column}`, `sample_rows` (first 3 rows by rowid, as `[]map[string]any`), `notes[]` (static per table, e.g. customers: "no country/address column; phone numbers are +7 format").

Table name is validated against the live table list before being interpolated into `pragma_table_info(?)` / `SELECT … FROM "<name>"` — never concatenate unvalidated input.

### 4.3 `query`
Use for anything the specialized tools don't cover. Arbitrary read-only SQLite `SELECT` or `WITH … SELECT`.

In:
| field | type | default | constraints |
|---|---|---|---|
| `sql` | string | — | required, one statement, ≤ 10 000 chars |
| `limit` | int | 100 | 1…500 |
| `offset` | int | 0 | ≥ 0 |

Out: `columns []string`, `rows [][]any` (positional, matching `columns`), `row_count` (rows returned), `truncated bool`, `next_offset *int` (set only when `truncated`).

Execution: the SQL is **not wrapped** in a subquery (keeps `ORDER BY` semantics and leaves the guard as the single gate). The store iterates the cursor, skips `offset` rows, collects `limit`, then reads one more row to compute `truncated`, and closes the cursor. Description tells the agent to add `ORDER BY` when paginating and to prefer aggregation over pulling raw rows.

Value mapping: `[]byte` → string, `NULL` → `null`, integers → int64, reals → float64.

### 4.4 `top_customers`
Answers "who spent the most", "who placed the most orders".

In: `by` enum `spent|orders` (default `spent`), `year` int (optional, 2000…2100), `status` []enum (optional, default = non-cancelled), `limit` int (default 5, 1…50).

Out:
```json
{
  "by": "spent",
  "filters": {"statuses": ["new","processing","shipped","completed"], "year": null},
  "customers": [{"rank": 1, "customer_id": 95, "first_name": "Дмитрий", "last_name": "Харитонов", "email": "dmitriy.kharitonov845@mail.ru", "total_spent": 701780.0, "order_count": 8}],
  "ties_beyond_limit": 0
}
```
Sort: `by=spent` → total_spent, order_count; `by=orders` → order_count, total_spent; then customer_id.

### 4.5 `top_products`
Answers "best-selling products", "top products by revenue".

In: `by` enum `units|revenue` (default `units`), `year`, `status`, `category` string (optional, exact match; unknown → tool error listing categories), `limit` (default 5, 1…50).

Out: `by`, `filters`, `products[] {rank, product_id, name, category, units_sold, revenue}`, `ties_beyond_limit`.
Sort: `units` → units_sold, revenue; `revenue` → revenue, units_sold; then product_id.

### 4.6 `revenue_summary`
Answers "revenue in YEAR", "top categories by revenue", revenue by month/status.

In: `group_by` enum `none|year|month|category|status` (default `none`), `year`, `status`, `limit` (default 50, 1…100; applies to groups).

Out:
```json
{
  "group_by": "category",
  "filters": {"statuses": ["new","processing","shipped","completed"], "year": null},
  "total": {"revenue": 28134150.0, "order_count": 648, "units_sold": 3295},
  "groups": [{"key": "Электроника", "revenue": 17060760.0, "order_count": 264, "units_sold": 674}],
  "data_range": {"first_order": "2026-02-17 18:53:30", "last_order": "2026-08-22 17:06:30"},
  "note": ""
}
```
- `groups` sorted by revenue DESC (except `year`/`month`, sorted by key ASC). `order_count` per group = `COUNT(DISTINCT orders.id)`.
- `month` key format `YYYY-MM`.
- If the filters match no orders, return zeros (not an error) and set `note`, e.g. `"No orders match year=2025. Orders exist from 2026-02-17 to 2026-08-22."`.
- No `country` grouping: the data does not exist (see §2).

Tool → question mapping (must hold with a single call, verified by integration tests against `testdata/golden.json`):

| Q | Call |
|---|---|
| 1 | `list_tables` |
| 2, 3 | `list_tables` / `describe_table{customers}` → agent reports that country is not stored |
| 4 | `top_customers{by: spent, limit: 1}` |
| 5 | `top_products{by: units, limit: 5}` |
| 6 | `revenue_summary{group_by: category, limit: 3}` |
| 7 | `revenue_summary{year: 2025}` → 0 + note |
| 8 | `top_customers{by: orders, limit: 1}` |

### 4.7 Server instructions
`ServerOptions.Instructions` (≤ ~800 chars): what the DB is, that access is read-only, the revenue rule and default status filter, "prefer specialized tools; use `query` for anything else; call `describe_table` before writing SQL", date format, and that country data does not exist.

## 5. Security (defense in depth)

Each layer must stop a write on its own; tests cover each independently.

1. **Connection.** DSN `file:<path>?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(2000)`. Path is URL-escaped. `SetMaxOpenConns(4)`. Even if the guard is bypassed, SQLite refuses writes (`attempt to write a readonly database`).
2. **SQL guard** (`internal/sqlguard`, pure function `Check(sql string) error`):
   - Lexer that understands `'…'` strings (with `''` escapes), `"…"`, `` `…` ``, `[…]` identifiers, `-- …\n` and `/* … */` comments (unterminated string/comment → reject).
   - Comments are dropped; literals and quoted identifiers become opaque tokens (so `WHERE note = 'DROP TABLE'` and `SELECT 1 AS "delete"` pass).
   - Exactly one statement: a single trailing `;` is allowed, any `;` followed by another token → reject.
   - First keyword must be `SELECT` or `WITH`.
   - Any bare keyword token from the deny list anywhere (including inside CTEs) → reject: `INSERT UPDATE DELETE REPLACE MERGE UPSERT DROP ALTER CREATE ATTACH DETACH PRAGMA VACUUM REINDEX ANALYZE BEGIN COMMIT ROLLBACK SAVEPOINT RELEASE TRUNCATE`. Exception: `REPLACE` immediately followed by `(` is the scalar function and allowed.
   - Deny function `load_extension`.
   - Errors are typed: `*sqlguard.Error{Reason, Keyword}`; `errors.Is(err, sqlguard.ErrReadOnly)` for write attempts.
3. **Resource limits.** Each tool call runs under `context.WithTimeout` (default 5s, `--query-timeout`); modernc interrupts the statement on cancellation. Row cap 500 for `query`, 50/100 for analytics tools. SQL length cap 10 000.

Specialized tools use only static SQL with `?` placeholders; user values never reach SQL text (enums/table names are validated against allow-lists).

**Expected behaviour for "Delete all cancelled orders":** the agent has no write tool; if it calls `query` with `DELETE …`, the result is `isError: true` with
`Refused: this server provides read-only access. DELETE statements are not allowed; only a single SELECT or WITH … SELECT query can be run. To see the affected rows use: SELECT … FROM orders WHERE status = 'cancelled'.`
The DB file checksum is unchanged.

## 6. Errors

- Every failure visible to the agent is a tool error (`isError: true`): one human-readable sentence + a hint. No stack traces, no Go error chains, no filesystem paths, no driver prefixes like `SQL logic error:`.
- `internal/tools` maps errors to messages; full errors (with `%w` chain) are logged to stderr via slog.

| Cause | Message to agent (shape) |
|---|---|
| guard: write keyword | `Refused: this server provides read-only access. <KW> statements are not allowed …` |
| guard: multiple statements / not SELECT / unterminated | `Rejected: <reason>. Send exactly one SELECT or WITH … SELECT statement.` |
| `no such column: X` | `Column "X" does not exist. Available columns — customers: id, first_name, …; orders: …` (for tables referenced in the SQL; all tables if none detected) |
| `no such table: X` | `Table "X" does not exist. Available tables: customers, orders, order_items, products.` |
| syntax error | `SQL syntax error near "…". This is SQLite: use strftime('%Y', order_date) for dates, \|\| for concatenation.` |
| ambiguous column | `Column "X" is ambiguous; qualify it with a table alias.` |
| timeout | `Query exceeded the 5s time limit. Add filters or aggregate instead of scanning raw rows.` |
| unknown table / category / bad enum | list valid values |
| anything else | `Query failed: <sanitized sqlite message>.` |

Input-schema violations are rejected by the SDK before our handler (already a tool error).

## 7. Configuration & process

- DB path precedence: `--db` flag > `SHOP_DB_PATH` env > `./shop.db` in CWD > `shop.db` next to the executable (`os.Executable`). No absolute paths in code.
- Startup: resolve path → `os.Stat` → open → `PingContext` → `SELECT 1 FROM orders LIMIT 1`. On failure: one clear line on stderr (`shop-mcp: database not found at "<path>" (set --db or SHOP_DB_PATH)`) and exit code 1.
- Flags: `--db`, `--query-timeout` (duration, default `5s`), `--log-level` (`debug|info|warn|error`, default `info`), `--version`.
- Version string via `-ldflags "-X main.version=…"`, default `dev`; passed to `mcp.Implementation`.
- Graceful shutdown: `signal.NotifyContext(ctx, SIGINT, SIGTERM)` → `server.Run(ctx, &mcp.StdioTransport{})` returns → close DB → exit 0. Client disconnect (EOF) also exits 0.
- Logging: one `info` line per tool call (`tool`, `duration_ms`, `rows`, `error`); SQL text only at `debug`.

## 8. Code layout

```
cmd/shop-mcp/main.go        flags, config resolution, slog, signals, wiring
internal/store/             *sql.DB wrapper: Open, Tables, Describe, Query(ctx, sql, limit, offset), analytics queries
internal/sqlguard/          lexer + Check; no deps
internal/tools/             tool registration (Register(server, deps)), In/Out types, descriptions, error mapping
testdata/                   golden.sql, golden.json
examples/                   .mcp.json (Claude Code), claude_desktop_config.json, cursor-mcp.json
```
Rules: errors wrapped with `%w`; interfaces declared by the consumer (`internal/tools` defines the small interface it needs from the store); no globals except `version`; `context.Context` first arg everywhere; no `panic` on input.

## 9. Testing

`go test -race ./...` green; `golangci-lint run` clean.

- **sqlguard** — table-driven, both allow and deny cases, at minimum:
  allow: `SELECT 1`, `select * from orders;`, `WITH t AS (SELECT 1) SELECT * FROM t`, `SELECT 'DROP TABLE x'`, `SELECT 1 AS "delete"`, `SELECT replace(name,'a','b') FROM products`, `SELECT 1 -- delete\n`, `SELECT /* drop */ 1`, `SELECT 'it''s; DELETE'`.
  deny: `SELECT 1; DROP TABLE x`, `/* */ DELETE FROM orders`, `delete from orders`, `DeLeTe FROM orders`, `WITH x AS (DELETE FROM orders RETURNING *) SELECT * FROM x`, `WITH x AS (SELECT 1) DELETE FROM orders`, `WITH x AS (SELECT 1) REPLACE INTO t VALUES(1)`, `INSERT INTO t VALUES (1)`, `UPDATE orders SET status='x'`, `PRAGMA query_only=OFF`, `ATTACH 'x.db' AS y`, `VACUUM`, `SELECT load_extension('x')`, `SELECT 1;;`, `SELECT 'unterminated`, `SELECT /* unterminated`, empty / whitespace-only.
- **store** — against a temp copy of `shop.db`: write via raw `ExecContext("DELETE …")` fails (layer 1 without guard); pagination (`limit`, `offset`, `truncated`, `next_offset`) on `SELECT id FROM orders ORDER BY id`; timeout with an expensive recursive CTE and a 50ms timeout.
- **tools (integration)** — server + client over `mcp.NewInMemoryTransports()`, DB = temp copy of `../../shop.db`:
  - `ListTools` returns the 6 tools, each with a non-empty description and input schema, and `ReadOnlyHint`.
  - Every call from the table in §4 matches `testdata/golden.json` (Q5 including the tie order; Q7 = 0 with non-empty `note`).
  - `query` with `DELETE FROM orders WHERE status='cancelled'` → `IsError`, message contains `read-only`; SHA-256 of the DB file identical before/after; `orders` still 750 rows.
  - Error hints: `SELECT name FROM customers` → message lists `first_name, last_name`; unknown table; `describe_table{table:"nope"}`.
  - No error text contains the temp dir path or `goroutine`.

## 10. Tooling & docs

- `Makefile`: `build` (`CGO_ENABLED=0`, `-trimpath`, ldflags version → `bin/shop-mcp`), `test` (`-race`), `lint`, `run`, `golden` (re-runs `golden.sql`).
- `.golangci.yml` (v2 format): default linters + `errorlint, errcheck, gosec, revive, bodyclose, sqlclosecheck, rowserrcheck, gocritic, misspell`.
- `Dockerfile`: `golang:1.27` builder → `gcr.io/distroless/static-debian12:nonroot`; copies binary and `shop.db` to `/data/shop.db`, `ENV SHOP_DB_PATH=/data/shop.db`; run with `docker run -i --rm shop-mcp`.
- GitHub Actions `ci.yml` on push/PR: `setup-go` (`go-version-file: go.mod`), `golangci-lint-action`, `go test -race ./...`, `go build ./...`.
- `examples/`: Claude Code `.mcp.json` (binary and `go run ./cmd/shop-mcp` variants), Claude Desktop, Cursor. Paths in examples are placeholders (`/ABS/PATH/TO/shop-mcp`).
- `CLAUDE.md` (+ `AGENTS.md` symlink or short pointer): commands, layout, invariants (read-only layers, stdout = protocol, golden tests, revenue rule).
- `README.md` (English): what/why → install → configure (flags/env) → run → connect (Claude Code `claude mcp add shop -- /abs/path/bin/shop-mcp --db /abs/path/shop.db`, Desktop, Cursor, Docker) → tools table → security model (why each of 3 layers) → revenue rule & data caveats (no country, no 2025) → example dialogue with the 8 questions + delete refusal (real transcript from the live check) → development.

## 11. Delivery plan

Small conventional commits, no push without explicit OK:

1. `chore: init module, gitignore, shop.db, golden answers`
2. `docs: add SPEC.md`
3. `feat(sqlguard): lexer and read-only statement check` + tests
4. `feat(store): read-only sqlite store with pagination and timeouts` + tests
5. `feat(tools): list_tables, describe_table, query`
6. `feat(tools): top_customers, top_products, revenue_summary`
7. `feat(cmd): config resolution, logging, graceful shutdown`
8. `test(tools): in-memory MCP integration tests against golden answers`
9. `build: Makefile, golangci-lint config, Dockerfile`
10. `ci: lint and test workflow`
11. `docs: CLAUDE.md, agent config examples`
12. Live check in Claude Code (8 questions + delete) → `docs: README with live session transcript`

## 12. Out of scope

Write access of any kind, HTTP/SSE transport, auth, MCP resources/prompts, multi-DB support, caching.
