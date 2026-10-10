package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lama2x2/shop-mcp/internal/store"
)

var tableDescriptions = map[string]string{
	"customers":   "One row per customer: first_name, last_name, email (unique), phone, created_at. Full names are not unique; identify customers by id and email.",
	"products":    "One row per product: name, category, price (current list price, not used for revenue), stock_quantity, created_at.",
	"orders":      "One row per order: customer_id, order_date, status (new|processing|shipped|completed|cancelled), total_amount = sum of its items.",
	"order_items": "One row per order line: order_id, product_id, quantity, unit_price (price at purchase time). Revenue is computed from these rows.",
}

var tableNotes = map[string][]string{
	"customers": {
		"There is no country, city or address column; questions about customer location cannot be answered from the data.",
		"Phone numbers are in +7 format.",
	},
	"products": {
		"Names and categories are in Russian.",
		"price is the current list price; use order_items.unit_price for revenue.",
	},
	"orders": {
		"order_date is TEXT 'YYYY-MM-DD HH:MM:SS'; filter years with order_date >= 'YYYY-01-01' AND order_date < 'YYYY+1-01-01' or strftime('%Y', order_date).",
		"Revenue excludes cancelled orders by default.",
	},
	"order_items": {
		"Line revenue = quantity * unit_price.",
	},
}

const revenueRule = "Revenue = SUM(order_items.quantity * order_items.unit_price) over orders with status in (new, processing, shipped, completed); cancelled orders are excluded unless a status filter says otherwise."

// list_tables

type listTablesIn struct{}

type tableInfo struct {
	Name        string `json:"name"`
	RowCount    int64  `json:"row_count"`
	Description string `json:"description"`
}

type listTablesOut struct {
	Tables        []tableInfo `json:"tables"`
	Relationships []string    `json:"relationships"`
	DatasetNotes  []string    `json:"dataset_notes"`
}

func listTablesTool() *mcp.Tool {
	return &mcp.Tool{
		Name: "list_tables",
		Description: `List all tables of the shop database with row counts, a one-line description each, foreign-key relationships and dataset notes (order date range, order statuses, product categories, revenue rule, missing data).
Use this first to discover the database. Takes no input.`,
		InputSchema: inputSchema[listTablesIn](nil),
	}
}

func (ts *toolset) listTables(ctx context.Context, _ listTablesIn) (listTablesOut, int, error) {
	tables, err := ts.store.Tables(ctx)
	if err != nil {
		return listTablesOut{}, 0, err
	}
	out := listTablesOut{Tables: make([]tableInfo, 0, len(tables)), Relationships: []string{}}
	for _, t := range tables {
		out.Tables = append(out.Tables, tableInfo{Name: t.Name, RowCount: t.RowCount, Description: tableDescriptions[t.Name]})
	}

	rels, err := ts.store.Relationships(ctx)
	if err != nil {
		return listTablesOut{}, 0, err
	}
	for _, r := range rels {
		out.Relationships = append(out.Relationships, fmt.Sprintf("%s.%s → %s.%s", r.Table, r.Column, r.ReferencesTable, r.ReferencesColumn))
	}

	notes, err := ts.datasetNotes(ctx)
	if err != nil {
		return listTablesOut{}, 0, err
	}
	out.DatasetNotes = notes
	return out, len(out.Tables), nil
}

func (ts *toolset) datasetNotes(ctx context.Context) ([]string, error) {
	first, last, err := ts.store.OrderRange(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := ts.store.StatusCounts(ctx)
	if err != nil {
		return nil, err
	}
	cats, err := ts.store.Categories(ctx)
	if err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(counts))
	for _, c := range counts {
		parts = append(parts, fmt.Sprintf("%s %d", c.Status, c.Count))
	}
	notes := []string{}
	if first != "" {
		notes = append(notes, fmt.Sprintf("Orders span %s … %s; there are no orders outside this range.", first, last))
	}
	return append(notes,
		"Order statuses: "+strings.Join(parts, ", ")+".",
		"Product categories: "+strings.Join(cats, ", ")+".",
		"No country/address data is stored for customers.",
		revenueRule,
		"Dates are TEXT 'YYYY-MM-DD HH:MM:SS'. Money is not tagged with a currency (implied RUB).",
	), nil
}

// describe_table

type describeTableIn struct {
	Table string `json:"table" jsonschema:"Table name, e.g. \"orders\". Must be one of the tables returned by list_tables."`
}

type describeTableOut struct {
	Name         string             `json:"name"`
	Description  string             `json:"description"`
	RowCount     int64              `json:"row_count"`
	Columns      []store.Column     `json:"columns"`
	ForeignKeys  []store.ForeignKey `json:"foreign_keys"`
	ReferencedBy []store.Reference  `json:"referenced_by"`
	SampleRows   []map[string]any   `json:"sample_rows"`
	Notes        []string           `json:"notes"`
}

func describeTableTool() *mcp.Tool {
	return &mcp.Tool{
		Name: "describe_table",
		Description: `Describe one table: columns (name, type, not_null, primary_key, default), foreign keys, tables that reference it, the first 3 rows as samples, and notes about the data.
Call this before writing SQL for the query tool. Example: {"table": "orders"}.`,
		InputSchema: inputSchema[describeTableIn](nil),
	}
}

func (ts *toolset) describeTable(ctx context.Context, in describeTableIn) (describeTableOut, int, error) {
	d, err := ts.store.Describe(ctx, in.Table)
	if errors.Is(err, store.ErrNoSuchTable) {
		return describeTableOut{}, 0, userErrorf(err, "Table %q does not exist. Available tables: %s.", in.Table, ts.tablesHint(ctx))
	}
	if err != nil {
		return describeTableOut{}, 0, err
	}
	notes := tableNotes[d.Name]
	if notes == nil {
		notes = []string{}
	}
	return describeTableOut{
		Name:         d.Name,
		Description:  tableDescriptions[d.Name],
		RowCount:     d.RowCount,
		Columns:      d.Columns,
		ForeignKeys:  d.ForeignKeys,
		ReferencedBy: d.ReferencedBy,
		SampleRows:   d.SampleRows,
		Notes:        notes,
	}, len(d.SampleRows), nil
}

// query

const (
	defaultQueryLimit = 100
	maxQueryLimit     = 500
	maxSQLLength      = 10000
)

type queryIn struct {
	SQL    string `json:"sql" jsonschema:"One read-only SQLite SELECT or WITH … SELECT statement, e.g. SELECT status, COUNT(*) FROM orders GROUP BY status"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum rows to return (1-500, default 100)"`
	Offset int    `json:"offset,omitempty" jsonschema:"Rows to skip for pagination (default 0); use next_offset from the previous page"`
}

type queryOut struct {
	Columns    []string `json:"columns"`
	Rows       [][]any  `json:"rows"`
	RowCount   int      `json:"row_count"`
	Truncated  bool     `json:"truncated"`
	NextOffset *int     `json:"next_offset,omitempty"`
}

func queryTool() *mcp.Tool {
	return &mcp.Tool{
		Name: "query",
		Description: `Run one read-only SQLite query (SELECT or WITH … SELECT) for anything the specialized tools (top_customers, top_products, revenue_summary) don't cover. Writes of any kind are refused.
Returns columns, rows (arrays in column order), row_count, truncated and next_offset. At most "limit" rows (default 100, max 500) are returned; when truncated is true, call again with offset = next_offset, and always add ORDER BY when paginating. Prefer GROUP BY / aggregates over pulling raw rows.
Revenue rule: SUM(order_items.quantity * order_items.unit_price) over orders with status <> 'cancelled' (products.price is the current list price, not revenue). Dates are TEXT 'YYYY-MM-DD HH:MM:SS'; use strftime('%Y', order_date) for date parts. Call describe_table first to see column names.
Example: {"sql": "SELECT status, COUNT(*) AS n FROM orders GROUP BY status ORDER BY n DESC"}.`,
		InputSchema: inputSchema[queryIn](map[string]prop{
			"sql":    {maxLen: new(maxSQLLength)},
			"limit":  {def: defaultQueryLimit, min: num(1), max: num(maxQueryLimit)},
			"offset": {def: 0, min: num(0)},
		}),
	}
}

func (ts *toolset) query(ctx context.Context, in queryIn) (queryOut, int, error) {
	limit := in.Limit
	if limit == 0 {
		limit = defaultQueryLimit
	}
	ts.log.Debug("query", "sql", in.SQL, "limit", limit, "offset", in.Offset)
	res, err := ts.store.Query(ctx, in.SQL, limit, in.Offset)
	if err != nil {
		return queryOut{}, 0, userErrorf(err, "%s", ts.queryMessage(ctx, in.SQL, err))
	}
	return queryOut{
		Columns:    res.Columns,
		Rows:       res.Rows,
		RowCount:   len(res.Rows),
		Truncated:  res.Truncated,
		NextOffset: res.NextOffset,
	}, len(res.Rows), nil
}
