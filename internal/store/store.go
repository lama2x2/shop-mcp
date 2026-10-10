// Package store is a read-only wrapper around the shop SQLite database.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/lama2x2/shop-mcp/internal/sqlguard"
)

// Store gives read-only access to the shop database.
type Store struct {
	db *sql.DB
}

// DSN builds the read-only connection string for the database at path.
func DSN(path string) string {
	u := url.URL{Path: path}
	return "file:" + u.EscapedPath() + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(2000)"
}

// Open opens the database at path read-only and checks that it is reachable
// and contains the orders table.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", DSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(4)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	var one int
	if err := db.QueryRowContext(ctx, "SELECT 1 FROM orders LIMIT 1").Scan(&one); err != nil && !errors.Is(err, sql.ErrNoRows) {
		_ = db.Close()
		return nil, fmt.Errorf("check orders table: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying connection pool.
func (s *Store) Close() error {
	return s.db.Close()
}

// QueryResult is a page of rows from an ad-hoc query.
type QueryResult struct {
	Columns    []string
	Rows       [][]any
	Truncated  bool
	NextOffset *int
}

// Query runs a single read-only SELECT (checked by sqlguard) and returns at
// most limit rows after skipping offset rows. The SQL is executed as is, so
// its ORDER BY is preserved.
func (s *Store) Query(ctx context.Context, query string, limit, offset int) (*QueryResult, error) {
	if err := sqlguard.Check(query); err != nil {
		return nil, fmt.Errorf("guard: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, wrapCtx(ctx, "query", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}
	res := &QueryResult{Columns: cols, Rows: [][]any{}}

	for i := 0; i < offset; i++ {
		if !rows.Next() {
			return res, wrapCtx(ctx, "skip rows", rows.Err())
		}
	}
	for len(res.Rows) < limit && rows.Next() {
		row, err := scanRow(rows, len(cols))
		if err != nil {
			return nil, err
		}
		res.Rows = append(res.Rows, row)
	}
	if len(res.Rows) == limit && rows.Next() {
		res.Truncated = true
		next := offset + limit
		res.NextOffset = &next
	}
	if err := rows.Err(); err != nil {
		return nil, wrapCtx(ctx, "read rows", err)
	}
	return res, nil
}

// wrapCtx prefers the context error so callers can detect timeouts with
// errors.Is(err, context.DeadlineExceeded).
func wrapCtx(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s: %w (%w)", op, ctxErr, err)
	}
	return fmt.Errorf("%s: %w", op, err)
}

func scanRow(rows *sql.Rows, n int) ([]any, error) {
	vals := make([]any, n)
	ptrs := make([]any, n)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, fmt.Errorf("scan row: %w", err)
	}
	for i, v := range vals {
		vals[i] = normalize(v)
	}
	return vals, nil
}

// normalize maps driver values to JSON-friendly ones. The driver parses
// DATETIME columns into time.Time; they are formatted back to the format
// stored in the database.
func normalize(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case time.Time:
		if x.Nanosecond() != 0 {
			return x.Format("2006-01-02 15:04:05.999999999")
		}
		return x.Format(time.DateTime)
	default:
		return v
	}
}

// quoteIdent quotes an identifier that has already been validated against
// the live table list.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
