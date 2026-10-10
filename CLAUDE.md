# shop-mcp

Read-only MCP server (Go, stdio) over the SQLite shop database `shop.db`. The design is fixed in `SPEC.md`; change the spec first if behaviour must change.

## Commands

- `make build` — static binary at `bin/shop-mcp` (`CGO_ENABLED=0`, version via ldflags)
- `make test` — `go test -race ./...`
- `make lint` — `golangci-lint run ./...` (v2 config in `.golangci.yml`)
- `make run` — build and serve `./shop.db` on stdio
- `make golden` — re-run `testdata/golden.sql` with `sqlite3 -readonly`

## Layout

- `cmd/shop-mcp` — flags, DB path resolution (`--db` > `SHOP_DB_PATH` > `./shop.db` > next to the executable), slog to stderr, signals
- `internal/sqlguard` — lexer + `Check(sql)`; no dependencies
- `internal/store` — read-only `*sql.DB` wrapper: `Query` (guarded, paginated), schema introspection, analytics aggregates
- `internal/tools` — tool definitions, input schemas, descriptions, error → message mapping; defines the `Store` interface it consumes
- `testdata/golden.{sql,json}` — reference answers; integration tests compare against them

## Invariants

- **Read-only, three independent layers**: connection `mode=ro` + `query_only(1)`; `sqlguard.Check` inside `store.Query`; per-call timeout and row caps. Each layer has its own tests — keep them independent.
- **stdout is the MCP protocol.** Log only via slog to stderr. Never `fmt.Print*` to stdout.
- **Revenue** = `SUM(order_items.quantity * order_items.unit_price)` over orders with status in `new, processing, shipped, completed` unless the caller passes `status`. Never use `products.price` or `orders.total_amount` for revenue.
- Specialized tools use static SQL with `?` placeholders only; enum values and table names are validated against allow-lists.
- Tool failures are returned as plain `error` from handlers → `isError: true`. Messages are one sentence + hint, no paths, no driver prefixes, no Go chains; full errors go to the log.
- The dataset has no country/address data and no 2025 orders; tools must say so rather than guess.
- Golden tests (`internal/tools/tools_test.go`) must stay green; if `shop.db` changes, regenerate `golden.json` from `golden.sql` deliberately.
- No push without explicit OK; small conventional commits.
