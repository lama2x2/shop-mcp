package store

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lama2x2/shop-mcp/internal/sqlguard"
)

// openCopy opens a temp copy of the repository's shop.db.
func openCopy(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shop copy.db") // space checks DSN escaping
	src, err := os.Open(filepath.Join("..", "..", "shop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func TestOpenMissingFile(t *testing.T) {
	_, err := Open(context.Background(), filepath.Join(t.TempDir(), "missing.db"))
	if err == nil {
		t.Fatal("Open of a missing file succeeded")
	}
}

func TestConnectionRefusesWrites(t *testing.T) {
	s, _ := openCopy(t)
	ctx := context.Background()
	// Raw access on one pinned connection, bypassing the guard; query_only is
	// switched off so that mode=ro alone has to stop the write.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA query_only = OFF"); err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		"DELETE FROM orders WHERE status = 'cancelled'",
		"UPDATE orders SET status = 'new'",
		"CREATE TABLE x (id INTEGER)",
	}
	for _, q := range stmts {
		_, err := conn.ExecContext(ctx, q)
		if err == nil || !strings.Contains(err.Error(), "readonly") {
			t.Errorf("ExecContext(%q) error = %v, want readonly database error", q, err)
		}
	}
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM orders").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 750 {
		t.Fatalf("orders has %d rows, want 750", n)
	}
}

func TestQueryOnlyPragmaIsSet(t *testing.T) {
	s, _ := openCopy(t)
	var on int
	if err := s.db.QueryRowContext(context.Background(), "PRAGMA query_only").Scan(&on); err != nil {
		t.Fatal(err)
	}
	if on != 1 {
		t.Fatalf("query_only = %d, want 1", on)
	}
}

func TestQueryRejectsWritesViaGuard(t *testing.T) {
	s, _ := openCopy(t)
	_, err := s.Query(context.Background(), "DELETE FROM orders", 10, 0)
	if !errors.Is(err, sqlguard.ErrReadOnly) {
		t.Fatalf("Query(DELETE) error = %v, want ErrReadOnly", err)
	}
}

func TestQueryPagination(t *testing.T) {
	s, _ := openCopy(t)
	ctx := context.Background()
	const q = "SELECT id FROM orders ORDER BY id"

	res, err := s.Query(ctx, q, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 100 || !res.Truncated || res.NextOffset == nil || *res.NextOffset != 100 {
		t.Fatalf("first page: rows=%d truncated=%v next=%v", len(res.Rows), res.Truncated, res.NextOffset)
	}
	if got := res.Rows[0][0]; got != int64(1) {
		t.Fatalf("first id = %#v, want int64(1)", got)
	}
	if len(res.Columns) != 1 || res.Columns[0] != "id" {
		t.Fatalf("columns = %v", res.Columns)
	}

	res, err = s.Query(ctx, q, 100, 700)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 50 || res.Truncated || res.NextOffset != nil {
		t.Fatalf("last page: rows=%d truncated=%v next=%v", len(res.Rows), res.Truncated, res.NextOffset)
	}
	if got := res.Rows[0][0]; got != int64(701) {
		t.Fatalf("first id of last page = %#v, want 701", got)
	}

	res, err = s.Query(ctx, q, 50, 700)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 50 || res.Truncated {
		t.Fatalf("exact page: rows=%d truncated=%v", len(res.Rows), res.Truncated)
	}

	res, err = s.Query(ctx, q, 10, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 0 || res.Truncated {
		t.Fatalf("past end: rows=%d truncated=%v", len(res.Rows), res.Truncated)
	}
}

func TestQueryValueMapping(t *testing.T) {
	s, _ := openCopy(t)
	res, err := s.Query(context.Background(),
		"SELECT id, order_date, total_amount, NULL, CAST('ab' AS BLOB), status FROM orders ORDER BY id LIMIT 1", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	row := res.Rows[0]
	if _, ok := row[0].(int64); !ok {
		t.Errorf("id is %T, want int64", row[0])
	}
	if d, ok := row[1].(string); !ok || len(d) != len("2026-01-01 00:00:00") {
		t.Errorf("order_date = %#v, want YYYY-MM-DD HH:MM:SS string", row[1])
	}
	if _, ok := row[2].(float64); !ok {
		t.Errorf("total_amount is %T, want float64", row[2])
	}
	if row[3] != nil {
		t.Errorf("NULL = %#v, want nil", row[3])
	}
	if row[4] != "ab" {
		t.Errorf("blob = %#v, want \"ab\"", row[4])
	}
}

func TestQueryTimeout(t *testing.T) {
	s, _ := openCopy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Query(ctx,
		"WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM r) SELECT COUNT(*) FROM r", 1, 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("query was not interrupted promptly (%v)", d)
	}
}

func TestDescribe(t *testing.T) {
	s, _ := openCopy(t)
	ctx := context.Background()
	d, err := s.Describe(ctx, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if d.RowCount != 750 || len(d.Columns) != 5 || len(d.SampleRows) != 3 {
		t.Fatalf("describe orders: rows=%d cols=%d samples=%d", d.RowCount, len(d.Columns), len(d.SampleRows))
	}
	if len(d.ForeignKeys) != 1 || d.ForeignKeys[0].ReferencesTable != "customers" || d.ForeignKeys[0].OnDelete != "CASCADE" {
		t.Fatalf("foreign keys = %+v", d.ForeignKeys)
	}
	if len(d.ReferencedBy) != 1 || d.ReferencedBy[0] != (Reference{Table: "order_items", Column: "order_id"}) {
		t.Fatalf("referenced by = %+v", d.ReferencedBy)
	}
	if _, err := s.Describe(ctx, `orders"; DROP TABLE x; --`); !errors.Is(err, ErrNoSuchTable) {
		t.Fatalf("describe of unknown table: %v", err)
	}
}

func TestTables(t *testing.T) {
	s, _ := openCopy(t)
	tables, err := s.Tables(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"customers": 150, "products": 50, "orders": 750, "order_items": 1900}
	if len(tables) != len(want) {
		t.Fatalf("tables = %+v", tables)
	}
	for _, tb := range tables {
		if want[tb.Name] != tb.RowCount {
			t.Errorf("%s: %d rows, want %d", tb.Name, tb.RowCount, want[tb.Name])
		}
	}
}
