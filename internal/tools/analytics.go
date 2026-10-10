package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lama2x2/shop-mcp/internal/store"
)

const (
	defaultTopLimit    = 5
	maxTopLimit        = 50
	defaultGroupsLimit = 50
	maxGroupsLimit     = 100
	minYear, maxYear   = 2000, 2100
)

// Filters echoes the rule applied to an analytics response.
type Filters struct {
	Statuses []string `json:"statuses"`
	Year     *int     `json:"year"`
	Category *string  `json:"category,omitempty"`
}

// filter normalizes statuses (default, dedupe, canonical order) and builds
// the store filter plus its echo.
func filter(statuses []string, year *int, category string) (store.Filter, Filters) {
	sts := store.DefaultStatuses
	if len(statuses) > 0 {
		sts = slices.DeleteFunc(slices.Clone(store.Statuses), func(s string) bool { return !slices.Contains(statuses, s) })
	}
	f := store.Filter{Statuses: sts, Year: year, Category: category}
	echo := Filters{Statuses: sts, Year: year}
	if category != "" {
		echo.Category = &category
	}
	return f, echo
}

// cut returns the first limit items and how many excluded items tie with
// the last returned one on the primary metric.
func cut[T any, M comparable](items []T, limit int, primary func(T) M) ([]T, int) {
	if len(items) <= limit {
		return items, 0
	}
	last := primary(items[limit-1])
	ties := 0
	for _, it := range items[limit:] {
		if primary(it) != last {
			break
		}
		ties++
	}
	return items[:limit], ties
}

func orDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// top_customers

type topCustomersIn struct {
	By     string   `json:"by,omitempty" jsonschema:"Ranking metric: spent (total revenue from the customer) or orders (number of orders). Default spent."`
	Year   *int     `json:"year,omitempty" jsonschema:"Only orders with order_date in this calendar year, e.g. 2026. Omit for all time."`
	Status []string `json:"status,omitempty" jsonschema:"Order statuses to include. Default: new, processing, shipped, completed (cancelled excluded)."`
	Limit  int      `json:"limit,omitempty" jsonschema:"Number of customers to return (1-50, default 5)."`
}

type customerRow struct {
	Rank       int     `json:"rank"`
	CustomerID int64   `json:"customer_id"`
	FirstName  string  `json:"first_name"`
	LastName   string  `json:"last_name"`
	Email      string  `json:"email"`
	TotalSpent float64 `json:"total_spent"`
	OrderCount int64   `json:"order_count"`
}

type topCustomersOut struct {
	By              string        `json:"by"`
	Filters         Filters       `json:"filters"`
	Customers       []customerRow `json:"customers"`
	TiesBeyondLimit int           `json:"ties_beyond_limit"`
}

func topCustomersTool() *mcp.Tool {
	return &mcp.Tool{
		Name: "top_customers",
		Description: `Rank customers by money spent or by number of orders. Answers "who spent the most", "who placed the most orders", "top 10 customers in 2026".
Returns rank, customer_id, name, email, total_spent and order_count per customer, the applied filters, and ties_beyond_limit (how many further customers tie with the last one returned; mention the tie if > 0).
total_spent = SUM(quantity * unit_price) of the customer's order items; by default cancelled orders are excluded. Ties are broken by the other metric, then customer_id. Names are not unique; refer to customers by name and email.
Examples: {"by": "spent", "limit": 1}; {"by": "orders", "year": 2026, "status": ["completed"]}.`,
		InputSchema: inputSchema[topCustomersIn](map[string]prop{
			"by":     {enum: []string{"spent", "orders"}, def: "spent"},
			"year":   {min: num(minYear), max: num(maxYear)},
			"status": {itemEnum: store.Statuses},
			"limit":  {def: defaultTopLimit, min: num(1), max: num(maxTopLimit)},
		}),
	}
}

func (ts *toolset) topCustomers(ctx context.Context, in topCustomersIn) (topCustomersOut, int, error) {
	by := strOr(in.By, "spent")
	f, echo := filter(in.Status, in.Year, "")
	stats, err := ts.store.CustomerStats(ctx, f, by)
	if err != nil {
		return topCustomersOut{}, 0, err
	}
	primary := func(c store.CustomerStat) float64 { return c.TotalSpent }
	if by == "orders" {
		primary = func(c store.CustomerStat) float64 { return float64(c.OrderCount) }
	}
	top, ties := cut(stats, orDefault(in.Limit, defaultTopLimit), primary)

	out := topCustomersOut{By: by, Filters: echo, Customers: make([]customerRow, 0, len(top)), TiesBeyondLimit: ties}
	for i, c := range top {
		out.Customers = append(out.Customers, customerRow{
			Rank: i + 1, CustomerID: c.ID, FirstName: c.FirstName, LastName: c.LastName,
			Email: c.Email, TotalSpent: c.TotalSpent, OrderCount: c.OrderCount,
		})
	}
	return out, len(out.Customers), nil
}

// top_products

type topProductsIn struct {
	By       string   `json:"by,omitempty" jsonschema:"Ranking metric: units (quantity sold) or revenue. Default units."`
	Year     *int     `json:"year,omitempty" jsonschema:"Only orders with order_date in this calendar year, e.g. 2026. Omit for all time."`
	Status   []string `json:"status,omitempty" jsonschema:"Order statuses to include. Default: new, processing, shipped, completed (cancelled excluded)."`
	Category string   `json:"category,omitempty" jsonschema:"Only products of this category, exact match in Russian, e.g. Электроника. list_tables shows all categories."`
	Limit    int      `json:"limit,omitempty" jsonschema:"Number of products to return (1-50, default 5)."`
}

type productRow struct {
	Rank      int     `json:"rank"`
	ProductID int64   `json:"product_id"`
	Name      string  `json:"name"`
	Category  string  `json:"category"`
	UnitsSold int64   `json:"units_sold"`
	Revenue   float64 `json:"revenue"`
}

type topProductsOut struct {
	By              string       `json:"by"`
	Filters         Filters      `json:"filters"`
	Products        []productRow `json:"products"`
	TiesBeyondLimit int          `json:"ties_beyond_limit"`
}

func topProductsTool() *mcp.Tool {
	return &mcp.Tool{
		Name: "top_products",
		Description: `Rank products by units sold or by revenue. Answers "best-selling products", "top 5 products by revenue", "best sellers in a category".
Returns rank, product_id, name, category, units_sold and revenue per product, the applied filters, and ties_beyond_limit (how many further products tie with the last one returned; mention the tie if > 0).
revenue = SUM(quantity * unit_price) at purchase prices; by default cancelled orders are excluded. Ties are broken by the other metric, then product_id.
Examples: {"by": "units", "limit": 5}; {"by": "revenue", "category": "Электроника", "year": 2026}.`,
		InputSchema: inputSchema[topProductsIn](map[string]prop{
			"by":     {enum: []string{"units", "revenue"}, def: "units"},
			"year":   {min: num(minYear), max: num(maxYear)},
			"status": {itemEnum: store.Statuses},
			"limit":  {def: defaultTopLimit, min: num(1), max: num(maxTopLimit)},
		}),
	}
}

func (ts *toolset) topProducts(ctx context.Context, in topProductsIn) (topProductsOut, int, error) {
	by := strOr(in.By, "units")
	if in.Category != "" {
		cats, err := ts.store.Categories(ctx)
		if err != nil {
			return topProductsOut{}, 0, err
		}
		if !slices.Contains(cats, in.Category) {
			return topProductsOut{}, 0, userErrorf(nil, "Category %q does not exist. Available categories: %s.", in.Category, strings.Join(cats, ", "))
		}
	}
	f, echo := filter(in.Status, in.Year, in.Category)
	stats, err := ts.store.ProductStats(ctx, f, by)
	if err != nil {
		return topProductsOut{}, 0, err
	}
	primary := func(p store.ProductStat) float64 { return float64(p.UnitsSold) }
	if by == "revenue" {
		primary = func(p store.ProductStat) float64 { return p.Revenue }
	}
	top, ties := cut(stats, orDefault(in.Limit, defaultTopLimit), primary)

	out := topProductsOut{By: by, Filters: echo, Products: make([]productRow, 0, len(top)), TiesBeyondLimit: ties}
	for i, p := range top {
		out.Products = append(out.Products, productRow{
			Rank: i + 1, ProductID: p.ID, Name: p.Name, Category: p.Category,
			UnitsSold: p.UnitsSold, Revenue: p.Revenue,
		})
	}
	return out, len(out.Products), nil
}

// revenue_summary

type revenueSummaryIn struct {
	GroupBy string   `json:"group_by,omitempty" jsonschema:"How to split revenue: none (total only), year, month (key YYYY-MM), category or status. Default none."`
	Year    *int     `json:"year,omitempty" jsonschema:"Only orders with order_date in this calendar year, e.g. 2026. Omit for all time."`
	Status  []string `json:"status,omitempty" jsonschema:"Order statuses to include. Default: new, processing, shipped, completed (cancelled excluded)."`
	Limit   int      `json:"limit,omitempty" jsonschema:"Maximum number of groups (1-100, default 50)."`
}

type revenueTotals struct {
	Revenue    float64 `json:"revenue"`
	OrderCount int64   `json:"order_count"`
	UnitsSold  int64   `json:"units_sold"`
}

type revenueGroup struct {
	Key string `json:"key"`
	revenueTotals
}

type dataRange struct {
	FirstOrder string `json:"first_order"`
	LastOrder  string `json:"last_order"`
}

type revenueSummaryOut struct {
	GroupBy     string         `json:"group_by"`
	Filters     Filters        `json:"filters"`
	Total       revenueTotals  `json:"total"`
	Groups      []revenueGroup `json:"groups"`
	GroupsTotal int            `json:"groups_total"`
	DataRange   dataRange      `json:"data_range"`
	Note        string         `json:"note"`
}

func revenueSummaryTool() *mcp.Tool {
	return &mcp.Tool{
		Name: "revenue_summary",
		Description: `Total revenue, optionally split by year, month, category or status. Answers "revenue in 2026", "top 3 categories by revenue", "revenue by month", "how much was cancelled".
Returns total {revenue, order_count, units_sold}, groups (same fields plus key; category/status groups sorted by revenue descending, year/month groups chronologically), groups_total (groups before the limit), the applied filters, data_range (first and last order date in the whole database) and note (set when nothing matches, e.g. a year without orders: revenue is then 0, not an error).
Revenue = SUM(order_items.quantity * order_items.unit_price); by default orders with status cancelled are excluded. order_count counts distinct orders. There is no country data, so revenue cannot be split by country.
Examples: {"group_by": "category", "limit": 3}; {"year": 2025}; {"group_by": "status", "status": ["new","processing","shipped","completed","cancelled"]}.`,
		InputSchema: inputSchema[revenueSummaryIn](map[string]prop{
			"group_by": {enum: []string{"none", "year", "month", "category", "status"}, def: "none"},
			"year":     {min: num(minYear), max: num(maxYear)},
			"status":   {itemEnum: store.Statuses},
			"limit":    {def: defaultGroupsLimit, min: num(1), max: num(maxGroupsLimit)},
		}),
	}
}

func (ts *toolset) revenueSummary(ctx context.Context, in revenueSummaryIn) (revenueSummaryOut, int, error) {
	groupBy := strOr(in.GroupBy, "none")
	f, echo := filter(in.Status, in.Year, "")

	total, err := ts.store.RevenueTotal(ctx, f)
	if err != nil {
		return revenueSummaryOut{}, 0, err
	}
	first, last, err := ts.store.OrderRange(ctx)
	if err != nil {
		return revenueSummaryOut{}, 0, err
	}
	out := revenueSummaryOut{
		GroupBy:   groupBy,
		Filters:   echo,
		Total:     revenueTotals(total),
		Groups:    []revenueGroup{},
		DataRange: dataRange{FirstOrder: first, LastOrder: last},
	}

	if groupBy != "none" {
		groups, err := ts.store.RevenueGroups(ctx, f, groupBy)
		if err != nil {
			return revenueSummaryOut{}, 0, err
		}
		out.GroupsTotal = len(groups)
		if limit := orDefault(in.Limit, defaultGroupsLimit); len(groups) > limit {
			groups = groups[:limit]
		}
		for _, g := range groups {
			out.Groups = append(out.Groups, revenueGroup{Key: g.Key, revenueTotals: revenueTotals(g.RevenueTotals)})
		}
	}

	if total.OrderCount == 0 {
		out.Note = noMatchNote(echo, first, last)
	}
	return out, len(out.Groups), nil
}

func noMatchNote(f Filters, first, last string) string {
	var conds []string
	if f.Year != nil {
		conds = append(conds, fmt.Sprintf("year=%d", *f.Year))
	}
	if !slices.Equal(f.Statuses, store.DefaultStatuses) {
		conds = append(conds, "status="+strings.Join(f.Statuses, ","))
	}
	what := "No orders match the filters"
	if len(conds) > 0 {
		what = "No orders match " + strings.Join(conds, ", ")
	}
	if first == "" {
		return what + ". The database contains no orders."
	}
	return fmt.Sprintf("%s. Orders exist from %s to %s.", what, dateOnly(first), dateOnly(last))
}

func dateOnly(ts string) string {
	if d, _, ok := strings.Cut(ts, " "); ok {
		return d
	}
	return ts
}

// strOr returns v, or def when v is empty.
func strOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
