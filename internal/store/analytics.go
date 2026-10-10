package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Statuses are all order statuses allowed by the orders CHECK constraint.
var Statuses = []string{"new", "processing", "shipped", "completed", "cancelled"}

// DefaultStatuses is the revenue status filter: everything except cancelled.
var DefaultStatuses = []string{"new", "processing", "shipped", "completed"}

const revenueExpr = "ROUND(COALESCE(SUM(oi.quantity * oi.unit_price), 0), 2)"

// Filter restricts which orders and items are aggregated. Values are always
// bound as parameters; only the number of placeholders depends on input.
type Filter struct {
	Statuses []string // required, non-empty
	Year     *int     // optional, applied to orders.order_date
	Category string   // optional, exact products.category match
}

func (f Filter) where() (string, []any, error) {
	if len(f.Statuses) == 0 {
		return "", nil, fmt.Errorf("filter: no statuses")
	}
	conds := []string{"o.status IN (" + strings.TrimSuffix(strings.Repeat("?,", len(f.Statuses)), ",") + ")"}
	args := make([]any, 0, len(f.Statuses)+3)
	for _, st := range f.Statuses {
		args = append(args, st)
	}
	if f.Year != nil {
		conds = append(conds, "o.order_date >= ? AND o.order_date < ?")
		args = append(args, fmt.Sprintf("%04d-01-01", *f.Year), fmt.Sprintf("%04d-01-01", *f.Year+1))
	}
	if f.Category != "" {
		conds = append(conds, "p.category = ?")
		args = append(args, f.Category)
	}
	return " WHERE " + strings.Join(conds, " AND "), args, nil
}

const itemsJoin = `
FROM order_items oi
JOIN orders o   ON o.id = oi.order_id
JOIN products p ON p.id = oi.product_id`

// CustomerStat is a customer with aggregated spend and order count.
type CustomerStat struct {
	ID         int64
	FirstName  string
	LastName   string
	Email      string
	TotalSpent float64
	OrderCount int64
}

var customerOrder = map[string]string{
	"spent":  "total_spent DESC, order_count DESC, c.id",
	"orders": "order_count DESC, total_spent DESC, c.id",
}

// CustomerStats returns every customer with at least one matching order,
// ranked by "spent" or "orders" with deterministic tie-breaks.
func (s *Store) CustomerStats(ctx context.Context, f Filter, by string) ([]CustomerStat, error) {
	order, ok := customerOrder[by]
	if !ok {
		return nil, fmt.Errorf("customer stats: unknown ranking %q", by)
	}
	where, args, err := f.where()
	if err != nil {
		return nil, err
	}
	q := `SELECT c.id, c.first_name, c.last_name, c.email,
       ` + revenueExpr + ` AS total_spent,
       COUNT(DISTINCT o.id) AS order_count` + itemsJoin + `
JOIN customers c ON c.id = o.customer_id` + where + `
GROUP BY c.id
ORDER BY ` + order
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrapCtx(ctx, "customer stats", err)
	}
	defer rows.Close()
	var out []CustomerStat
	for rows.Next() {
		var c CustomerStat
		if err := rows.Scan(&c.ID, &c.FirstName, &c.LastName, &c.Email, &c.TotalSpent, &c.OrderCount); err != nil {
			return nil, fmt.Errorf("scan customer stat: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapCtx(ctx, "customer stats", err)
	}
	return out, nil
}

// ProductStat is a product with aggregated units sold and revenue.
type ProductStat struct {
	ID        int64
	Name      string
	Category  string
	UnitsSold int64
	Revenue   float64
}

var productOrder = map[string]string{
	"units":   "units_sold DESC, revenue DESC, p.id",
	"revenue": "revenue DESC, units_sold DESC, p.id",
}

// ProductStats returns every product with at least one matching sale,
// ranked by "units" or "revenue" with deterministic tie-breaks.
func (s *Store) ProductStats(ctx context.Context, f Filter, by string) ([]ProductStat, error) {
	order, ok := productOrder[by]
	if !ok {
		return nil, fmt.Errorf("product stats: unknown ranking %q", by)
	}
	where, args, err := f.where()
	if err != nil {
		return nil, err
	}
	q := `SELECT p.id, p.name, p.category,
       SUM(oi.quantity) AS units_sold,
       ` + revenueExpr + ` AS revenue` + itemsJoin + where + `
GROUP BY p.id
ORDER BY ` + order
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrapCtx(ctx, "product stats", err)
	}
	defer rows.Close()
	var out []ProductStat
	for rows.Next() {
		var p ProductStat
		if err := rows.Scan(&p.ID, &p.Name, &p.Category, &p.UnitsSold, &p.Revenue); err != nil {
			return nil, fmt.Errorf("scan product stat: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapCtx(ctx, "product stats", err)
	}
	return out, nil
}

// RevenueTotals are revenue, distinct order count and units for a filter.
type RevenueTotals struct {
	Revenue    float64
	OrderCount int64
	UnitsSold  int64
}

// RevenueGroup is RevenueTotals for one group key.
type RevenueGroup struct {
	Key string
	RevenueTotals
}

const totalsCols = revenueExpr + `, COUNT(DISTINCT o.id), COALESCE(SUM(oi.quantity), 0)`

// RevenueTotal aggregates all matching items.
func (s *Store) RevenueTotal(ctx context.Context, f Filter) (RevenueTotals, error) {
	where, args, err := f.where()
	if err != nil {
		return RevenueTotals{}, err
	}
	var t RevenueTotals
	err = s.db.QueryRowContext(ctx, "SELECT "+totalsCols+itemsJoin+where, args...).
		Scan(&t.Revenue, &t.OrderCount, &t.UnitsSold)
	if err != nil {
		return RevenueTotals{}, wrapCtx(ctx, "revenue total", err)
	}
	return t, nil
}

var groupExprs = map[string]struct{ key, order string }{
	"year":     {"strftime('%Y', o.order_date)", "1"},
	"month":    {"strftime('%Y-%m', o.order_date)", "1"},
	"category": {"p.category", "2 DESC, 1"},
	"status":   {"o.status", "2 DESC, 1"},
}

// RevenueGroups aggregates matching items per year, month, category or
// status. Year and month groups are ordered by key, others by revenue.
func (s *Store) RevenueGroups(ctx context.Context, f Filter, groupBy string) ([]RevenueGroup, error) {
	g, ok := groupExprs[groupBy]
	if !ok {
		return nil, fmt.Errorf("revenue groups: unknown grouping %q", groupBy)
	}
	where, args, err := f.where()
	if err != nil {
		return nil, err
	}
	q := "SELECT " + g.key + ", " + totalsCols + itemsJoin + where + " GROUP BY 1 ORDER BY " + g.order
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrapCtx(ctx, "revenue groups", err)
	}
	defer rows.Close()
	var out []RevenueGroup
	for rows.Next() {
		var r RevenueGroup
		if err := rows.Scan(&r.Key, &r.Revenue, &r.OrderCount, &r.UnitsSold); err != nil {
			return nil, fmt.Errorf("scan revenue group: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapCtx(ctx, "revenue groups", err)
	}
	return out, nil
}

// Categories returns the distinct product categories, sorted.
func (s *Store) Categories(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT category FROM products ORDER BY category")
	if err != nil {
		return nil, wrapCtx(ctx, "categories", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapCtx(ctx, "categories", err)
	}
	return out, nil
}

// OrderRange returns the first and last order_date over all orders; both are
// empty when there are no orders.
func (s *Store) OrderRange(ctx context.Context) (first, last string, err error) {
	var f, l sql.NullString
	if err := s.db.QueryRowContext(ctx, "SELECT MIN(order_date), MAX(order_date) FROM orders").Scan(&f, &l); err != nil {
		return "", "", wrapCtx(ctx, "order range", err)
	}
	return f.String, l.String, nil
}

// StatusCount is the number of orders with a status.
type StatusCount struct {
	Status string
	Count  int64
}

// StatusCounts returns order counts per status, in Statuses order.
func (s *Store) StatusCounts(ctx context.Context) ([]StatusCount, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT status, COUNT(*) FROM orders GROUP BY status")
	if err != nil {
		return nil, wrapCtx(ctx, "status counts", err)
	}
	defer rows.Close()
	counts := map[string]int64{}
	for rows.Next() {
		var (
			st string
			n  int64
		)
		if err := rows.Scan(&st, &n); err != nil {
			return nil, fmt.Errorf("scan status count: %w", err)
		}
		counts[st] = n
	}
	if err := rows.Err(); err != nil {
		return nil, wrapCtx(ctx, "status counts", err)
	}
	out := make([]StatusCount, 0, len(Statuses))
	for _, st := range Statuses {
		out = append(out, StatusCount{Status: st, Count: counts[st]})
	}
	return out, nil
}
