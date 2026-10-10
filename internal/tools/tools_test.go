package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lama2x2/shop-mcp/internal/store"
)

type env struct {
	cs     *mcp.ClientSession
	dbPath string
	dir    string
}

// setup serves the tools over in-memory transports, backed by a temp copy
// of the repository's shop.db.
func setup(t *testing.T, timeout time.Duration) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "shop.db")
	copyFile(t, filepath.Join("..", "..", "shop.db"), dbPath)

	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	server := mcp.NewServer(&mcp.Implementation{Name: "shop-mcp", Version: "test"}, &mcp.ServerOptions{Instructions: Instructions})
	Register(server, Deps{Store: st, Timeout: timeout})
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &env{cs: cs, dbPath: dbPath, dir: dir}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func (e *env) raw(t *testing.T, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := e.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): protocol error: %v", name, err)
	}
	return res
}

// call invokes a tool that must succeed and decodes its structured output.
func call[Out any](t *testing.T, e *env, name string, args map[string]any) Out {
	t.Helper()
	res := e.raw(t, name, args)
	if res.IsError {
		t.Fatalf("%s(%v) returned tool error: %s", name, args, text(res))
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out Out
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode %s output: %v\n%s", name, err, b)
	}
	return out
}

// callErr invokes a tool that must fail and returns the error text.
func callErr(t *testing.T, e *env, name string, args map[string]any) string {
	t.Helper()
	res := e.raw(t, name, args)
	if !res.IsError {
		t.Fatalf("%s(%v) succeeded, want tool error", name, args)
	}
	msg := text(res)
	if strings.Contains(msg, e.dir) || strings.Contains(msg, "goroutine") || strings.Contains(msg, "SQL logic error") {
		t.Errorf("%s(%v) error leaks internals: %q", name, args, msg)
	}
	return msg
}

func text(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

type golden struct {
	Q1 []struct {
		Name     string `json:"name"`
		RowCount int64  `json:"row_count"`
	} `json:"q1_tables"`
	Q4 struct {
		CustomerID int64   `json:"customer_id"`
		FirstName  string  `json:"first_name"`
		LastName   string  `json:"last_name"`
		Email      string  `json:"email"`
		TotalSpent float64 `json:"total_spent"`
		OrderCount int64   `json:"order_count"`
	} `json:"q4_top_customer_by_spent"`
	Q5 []struct {
		Rank      int     `json:"rank"`
		ProductID int64   `json:"product_id"`
		Name      string  `json:"name"`
		UnitsSold int64   `json:"units_sold"`
		Revenue   float64 `json:"revenue"`
	} `json:"q5_top_products_by_units"`
	Q6 []struct {
		Category  string  `json:"category"`
		Revenue   float64 `json:"revenue"`
		UnitsSold int64   `json:"units_sold"`
	} `json:"q6_top_categories_by_revenue"`
	Q7 struct {
		Revenue    float64 `json:"revenue"`
		OrderCount int64   `json:"order_count"`
	} `json:"q7_revenue_2025"`
	Q8 struct {
		CustomerID int64  `json:"customer_id"`
		FirstName  string `json:"first_name"`
		LastName   string `json:"last_name"`
		Email      string `json:"email"`
		OrderCount int64  `json:"order_count"`
	} `json:"q8_top_customer_by_orders"`
	Alt struct {
		CompletedOnly altRule `json:"completed_only"`
		AllStatuses   altRule `json:"all_statuses"`
	} `json:"alternative_status_rules"`
}

type altRule struct {
	Q4 struct {
		CustomerID int64   `json:"customer_id"`
		TotalSpent float64 `json:"total_spent"`
	} `json:"q4"`
	Q5Top5Units   []int64 `json:"q5_top5_units"`
	Q7Revenue2026 float64 `json:"q7_revenue_2026"`
	Q8            struct {
		CustomerID int64 `json:"customer_id"`
		OrderCount int64 `json:"order_count"`
	} `json:"q8"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestListTools(t *testing.T) {
	e := setup(t, 0)
	res, err := e.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"describe_table", "list_tables", "query", "revenue_summary", "top_customers", "top_products"}
	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("%s: empty description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("%s: no input schema", tool.Name)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint ||
			tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("%s: annotations = %+v", tool.Name, tool.Annotations)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestStatusEnumInSchema(t *testing.T) {
	e := setup(t, 0)
	msg := callErr(t, e, "top_customers", map[string]any{"status": []string{"returned"}})
	if !strings.Contains(msg, "returned") && !strings.Contains(msg, "enum") {
		t.Errorf("status enum error = %q", msg)
	}
	callErr(t, e, "top_customers", map[string]any{"by": "revenue"})
	callErr(t, e, "top_products", map[string]any{"limit": 51})
	callErr(t, e, "query", map[string]any{"sql": "SELECT 1", "limit": 501})
	callErr(t, e, "revenue_summary", map[string]any{"group_by": "country"})
}

func TestQ1ListTables(t *testing.T) {
	e := setup(t, 0)
	g := loadGolden(t)
	out := call[listTablesOut](t, e, "list_tables", nil)
	if len(out.Tables) != len(g.Q1) {
		t.Fatalf("tables = %+v", out.Tables)
	}
	for i, want := range g.Q1 {
		got := out.Tables[i]
		if got.Name != want.Name || got.RowCount != want.RowCount || got.Description == "" {
			t.Errorf("table %d = %+v, want %+v", i, got, want)
		}
	}
	if !slices.Contains(out.Relationships, "orders.customer_id → customers.id") || len(out.Relationships) != 3 {
		t.Errorf("relationships = %v", out.Relationships)
	}
	notes := strings.Join(out.DatasetNotes, "\n")
	for _, s := range []string{"2026-02-17 18:53:30", "2026-08-22 17:06:30", "No country", "cancelled 102", "Электроника"} {
		if !strings.Contains(notes, s) {
			t.Errorf("dataset notes miss %q:\n%s", s, notes)
		}
	}
}

func TestQ2Q3NoCountry(t *testing.T) {
	e := setup(t, 0)
	out := call[describeTableOut](t, e, "describe_table", map[string]any{"table": "customers"})
	var cols []string
	for _, c := range out.Columns {
		cols = append(cols, c.Name)
	}
	if !slices.Equal(cols, []string{"id", "first_name", "last_name", "email", "phone", "created_at"}) {
		t.Fatalf("columns = %v", cols)
	}
	if len(out.SampleRows) != 3 || out.RowCount != 150 {
		t.Fatalf("samples = %d, rows = %d", len(out.SampleRows), out.RowCount)
	}
	if !strings.Contains(strings.Join(out.Notes, " "), "no country") {
		t.Errorf("notes = %v", out.Notes)
	}
	if len(out.ReferencedBy) != 1 || out.ReferencedBy[0].Table != "orders" {
		t.Errorf("referenced_by = %+v", out.ReferencedBy)
	}
}

func TestQ4TopCustomerBySpent(t *testing.T) {
	e := setup(t, 0)
	g := loadGolden(t).Q4
	out := call[topCustomersOut](t, e, "top_customers", map[string]any{"by": "spent", "limit": 1})
	if len(out.Customers) != 1 {
		t.Fatalf("customers = %+v", out.Customers)
	}
	c := out.Customers[0]
	if c.Rank != 1 || c.CustomerID != g.CustomerID || c.FirstName != g.FirstName || c.LastName != g.LastName ||
		c.Email != g.Email || c.TotalSpent != g.TotalSpent || c.OrderCount != g.OrderCount {
		t.Fatalf("got %+v, want %+v", c, g)
	}
	if out.By != "spent" || out.Filters.Year != nil || !slices.Equal(out.Filters.Statuses, store.DefaultStatuses) {
		t.Fatalf("by/filters = %s %+v", out.By, out.Filters)
	}
}

func TestQ5TopProductsByUnits(t *testing.T) {
	e := setup(t, 0)
	g := loadGolden(t).Q5
	out := call[topProductsOut](t, e, "top_products", map[string]any{"by": "units", "limit": 5})
	if len(out.Products) != len(g) {
		t.Fatalf("products = %+v", out.Products)
	}
	for i, want := range g {
		p := out.Products[i]
		if p.Rank != want.Rank || p.ProductID != want.ProductID || p.Name != want.Name ||
			p.UnitsSold != want.UnitsSold || p.Revenue != want.Revenue {
			t.Errorf("rank %d: got %+v, want %+v", i+1, p, want)
		}
	}
	// Product 17 also sold 83 units and is cut off by the limit.
	if out.TiesBeyondLimit != 1 {
		t.Errorf("ties_beyond_limit = %d, want 1", out.TiesBeyondLimit)
	}
}

func TestQ6TopCategories(t *testing.T) {
	e := setup(t, 0)
	g := loadGolden(t).Q6
	out := call[revenueSummaryOut](t, e, "revenue_summary", map[string]any{"group_by": "category", "limit": 3})
	if len(out.Groups) != 3 || out.GroupsTotal != 5 {
		t.Fatalf("groups = %+v (total %d)", out.Groups, out.GroupsTotal)
	}
	for i, want := range g {
		got := out.Groups[i]
		if got.Key != want.Category || got.Revenue != want.Revenue || got.UnitsSold != want.UnitsSold {
			t.Errorf("group %d: got %+v, want %+v", i, got, want)
		}
	}
	if out.Total.Revenue != 28134150.0 || out.Total.OrderCount != 648 || out.Note != "" {
		t.Errorf("total = %+v, note = %q", out.Total, out.Note)
	}
}

func TestQ7Revenue2025(t *testing.T) {
	e := setup(t, 0)
	g := loadGolden(t).Q7
	out := call[revenueSummaryOut](t, e, "revenue_summary", map[string]any{"year": 2025})
	if out.Total.Revenue != g.Revenue || out.Total.OrderCount != g.OrderCount || out.Total.UnitsSold != 0 {
		t.Fatalf("total = %+v", out.Total)
	}
	if out.Note == "" || !strings.Contains(out.Note, "2026-02-17") {
		t.Fatalf("note = %q", out.Note)
	}
	if out.Filters.Year == nil || *out.Filters.Year != 2025 {
		t.Fatalf("filters = %+v", out.Filters)
	}
}

func TestQ8TopCustomerByOrders(t *testing.T) {
	e := setup(t, 0)
	g := loadGolden(t).Q8
	out := call[topCustomersOut](t, e, "top_customers", map[string]any{"by": "orders", "limit": 1})
	c := out.Customers[0]
	if c.CustomerID != g.CustomerID || c.FirstName != g.FirstName || c.LastName != g.LastName ||
		c.Email != g.Email || c.OrderCount != g.OrderCount {
		t.Fatalf("got %+v, want %+v", c, g)
	}
}

func TestAlternativeStatusRules(t *testing.T) {
	e := setup(t, 0)
	g := loadGolden(t).Alt
	rules := map[string]struct {
		statuses []string
		want     altRule
	}{
		"completed_only": {[]string{"completed"}, g.CompletedOnly},
		"all_statuses":   {store.Statuses, g.AllStatuses},
	}
	for name, r := range rules {
		t.Run(name, func(t *testing.T) {
			cust := call[topCustomersOut](t, e, "top_customers", map[string]any{"by": "spent", "limit": 1, "status": r.statuses})
			if c := cust.Customers[0]; c.CustomerID != r.want.Q4.CustomerID || c.TotalSpent != r.want.Q4.TotalSpent {
				t.Errorf("q4 = %+v, want %+v", c, r.want.Q4)
			}
			prods := call[topProductsOut](t, e, "top_products", map[string]any{"by": "units", "status": r.statuses})
			var ids []int64
			for _, p := range prods.Products {
				ids = append(ids, p.ProductID)
			}
			if !slices.Equal(ids, r.want.Q5Top5Units) {
				t.Errorf("q5 = %v, want %v", ids, r.want.Q5Top5Units)
			}
			rev := call[revenueSummaryOut](t, e, "revenue_summary", map[string]any{"year": 2026, "status": r.statuses})
			if rev.Total.Revenue != r.want.Q7Revenue2026 {
				t.Errorf("q7 = %v, want %v", rev.Total.Revenue, r.want.Q7Revenue2026)
			}
			ord := call[topCustomersOut](t, e, "top_customers", map[string]any{"by": "orders", "limit": 1, "status": r.statuses})
			if c := ord.Customers[0]; c.CustomerID != r.want.Q8.CustomerID || c.OrderCount != r.want.Q8.OrderCount {
				t.Errorf("q8 = %+v, want %+v", c, r.want.Q8)
			}
		})
	}
}

func TestStatusFilterIsNormalized(t *testing.T) {
	e := setup(t, 0)
	out := call[revenueSummaryOut](t, e, "revenue_summary", map[string]any{
		"group_by": "status", "status": []string{"cancelled", "new", "new"},
	})
	if !slices.Equal(out.Filters.Statuses, []string{"new", "cancelled"}) {
		t.Fatalf("statuses = %v", out.Filters.Statuses)
	}
	if len(out.Groups) != 2 {
		t.Fatalf("groups = %+v", out.Groups)
	}
}

func TestRevenueByMonthIsChronological(t *testing.T) {
	e := setup(t, 0)
	out := call[revenueSummaryOut](t, e, "revenue_summary", map[string]any{"group_by": "month"})
	var keys []string
	var sum float64
	for _, g := range out.Groups {
		keys = append(keys, g.Key)
		sum += g.Revenue
	}
	if len(keys) != 7 || keys[0] != "2026-02" || keys[6] != "2026-08" || !slices.IsSorted(keys) {
		t.Fatalf("month keys = %v", keys)
	}
	if diff := sum - out.Total.Revenue; diff > 0.01 || diff < -0.01 {
		t.Fatalf("sum of months %v != total %v", sum, out.Total.Revenue)
	}
}

func TestTopProductsCategory(t *testing.T) {
	e := setup(t, 0)
	out := call[topProductsOut](t, e, "top_products", map[string]any{"by": "revenue", "category": "Электроника", "limit": 50})
	if len(out.Products) != 10 || out.Filters.Category == nil {
		t.Fatalf("products = %d, filters = %+v", len(out.Products), out.Filters)
	}
	for _, p := range out.Products {
		if p.Category != "Электроника" {
			t.Fatalf("product from other category: %+v", p)
		}
	}
	msg := callErr(t, e, "top_products", map[string]any{"category": "Electronics"})
	if !strings.Contains(msg, "Электроника") || !strings.Contains(msg, "Книги и канцелярия") {
		t.Errorf("unknown category error = %q", msg)
	}
}

func TestDeleteIsRefused(t *testing.T) {
	e := setup(t, 0)
	before := fileHash(t, e.dbPath)

	msg := callErr(t, e, "query", map[string]any{"sql": "DELETE FROM orders WHERE status='cancelled'"})
	if !strings.Contains(msg, "read-only") || !strings.Contains(msg, "DELETE") ||
		!strings.Contains(msg, "SELECT * FROM orders WHERE status='cancelled'") {
		t.Errorf("refusal = %q", msg)
	}
	for _, sql := range []string{
		"WITH x AS (SELECT 1) DELETE FROM orders",
		"SELECT 1; DELETE FROM orders",
		"UPDATE orders SET status = 'cancelled'",
		"DROP TABLE orders",
		"PRAGMA query_only = OFF",
	} {
		callErr(t, e, "query", map[string]any{"sql": sql})
	}

	out := call[queryOut](t, e, "query", map[string]any{
		"sql": "SELECT COUNT(*), SUM(status = 'cancelled') FROM orders",
	})
	if out.Rows[0][0] != float64(750) || out.Rows[0][1] != float64(102) {
		t.Fatalf("orders after delete attempts = %v", out.Rows)
	}
	if after := fileHash(t, e.dbPath); after != before {
		t.Fatal("database file changed")
	}
}

func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func TestQueryPagination(t *testing.T) {
	e := setup(t, 0)
	page := call[queryOut](t, e, "query", map[string]any{"sql": "SELECT id FROM orders ORDER BY id"})
	if page.RowCount != 100 || !page.Truncated || page.NextOffset == nil || *page.NextOffset != 100 {
		t.Fatalf("default page: count=%d truncated=%v next=%v", page.RowCount, page.Truncated, page.NextOffset)
	}
	page = call[queryOut](t, e, "query", map[string]any{"sql": "SELECT id FROM orders ORDER BY id", "limit": 500, "offset": 500})
	if page.RowCount != 250 || page.Truncated || page.NextOffset != nil || page.Rows[0][0] != float64(501) {
		t.Fatalf("last page: count=%d truncated=%v next=%v", page.RowCount, page.Truncated, page.NextOffset)
	}
	if !slices.Equal(page.Columns, []string{"id"}) {
		t.Fatalf("columns = %v", page.Columns)
	}
}

func TestErrorHints(t *testing.T) {
	e := setup(t, 0)
	cases := []struct {
		tool string
		args map[string]any
		want []string
	}{
		{"query", map[string]any{"sql": "SELECT name FROM customers"}, []string{`Column "name" does not exist`, "first_name", "last_name"}},
		{"query", map[string]any{"sql": "SELECT c.name FROM customers c JOIN orders o ON o.customer_id = c.id"}, []string{"customers:", "orders:", "order_date"}},
		{"query", map[string]any{"sql": "SELECT * FROM nope"}, []string{`Table "nope" does not exist`, "customers, products, orders, order_items"}},
		{"query", map[string]any{"sql": "SELECT FROM orders WHERE"}, []string{"syntax error", "strftime"}},
		{"query", map[string]any{"sql": "SELECT id FROM orders JOIN order_items ON order_items.order_id = orders.id"}, []string{`Column "id" is ambiguous`}},
		{"query", map[string]any{"sql": "SELECT year(order_date) FROM orders"}, []string{"Query failed: no such function"}},
		{"query", map[string]any{"sql": "SELECT 1; SELECT 2"}, []string{"Rejected: multiple statements"}},
		{"query", map[string]any{"sql": "SELECT 'oops"}, []string{"Rejected: unterminated"}},
		{"query", map[string]any{"sql": "   "}, []string{"Rejected: empty query"}},
		{"describe_table", map[string]any{"table": "nope"}, []string{`Table "nope" does not exist`, "order_items"}},
		{"describe_table", map[string]any{"table": "sqlite_master"}, []string{"does not exist"}},
	}
	for _, c := range cases {
		msg := callErr(t, e, c.tool, c.args)
		for _, w := range c.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%s(%v): message %q does not contain %q", c.tool, c.args, msg, w)
			}
		}
	}
}

func TestTimeout(t *testing.T) {
	e := setup(t, 50*time.Millisecond)
	msg := callErr(t, e, "query", map[string]any{
		"sql": "WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM r) SELECT COUNT(*) FROM r",
	})
	if !strings.Contains(msg, "50ms time limit") {
		t.Fatalf("timeout message = %q", msg)
	}
}
