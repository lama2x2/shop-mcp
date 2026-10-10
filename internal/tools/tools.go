// Package tools registers the shop MCP tools on a server and maps store
// errors to messages an AI agent can act on.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lama2x2/shop-mcp/internal/store"
)

// Store is the read-only data access the tools need.
type Store interface {
	TableNames(ctx context.Context) ([]string, error)
	Tables(ctx context.Context) ([]store.Table, error)
	Relationships(ctx context.Context) ([]store.Relationship, error)
	Columns(ctx context.Context, table string) ([]string, error)
	Describe(ctx context.Context, table string) (*store.TableDescription, error)
	Query(ctx context.Context, sql string, limit, offset int) (*store.QueryResult, error)
	CustomerStats(ctx context.Context, f store.Filter, by string) ([]store.CustomerStat, error)
	ProductStats(ctx context.Context, f store.Filter, by string) ([]store.ProductStat, error)
	RevenueTotal(ctx context.Context, f store.Filter) (store.RevenueTotals, error)
	RevenueGroups(ctx context.Context, f store.Filter, groupBy string) ([]store.RevenueGroup, error)
	Categories(ctx context.Context) ([]string, error)
	OrderRange(ctx context.Context) (first, last string, err error)
	StatusCounts(ctx context.Context) ([]store.StatusCount, error)
}

// Deps are the dependencies of the tools.
type Deps struct {
	Store Store
	// Timeout bounds each tool call; zero means DefaultTimeout.
	Timeout time.Duration
	// Logger receives one line per tool call; nil discards.
	Logger *slog.Logger
}

// DefaultTimeout is the per-call time limit used when Deps.Timeout is zero.
const DefaultTimeout = 5 * time.Second

// Instructions are sent to the client on initialize.
const Instructions = `Read-only access to an online shop SQLite database: customers, products (Russian names and categories), orders, order_items. Nothing can be modified.
Revenue = SUM(order_items.quantity * order_items.unit_price) over orders whose status is not 'cancelled' by default; pass "status" to change the filter. Money is in RUB, rounded to 2 decimals.
Prefer the specialized tools (top_customers, top_products, revenue_summary). Use query for anything else, and call describe_table before writing SQL. Dates are TEXT 'YYYY-MM-DD HH:MM:SS'; use strftime() for date parts.
No country or address data is stored for customers: say so instead of guessing.`

type toolset struct {
	store   Store
	timeout time.Duration
	log     *slog.Logger
}

// Register adds all shop tools to s.
func Register(s *mcp.Server, d Deps) {
	ts := &toolset{store: d.Store, timeout: d.Timeout, log: d.Logger}
	if ts.timeout <= 0 {
		ts.timeout = DefaultTimeout
	}
	if ts.log == nil {
		ts.log = slog.New(slog.DiscardHandler)
	}
	add(s, ts, listTablesTool(), ts.listTables)
	add(s, ts, describeTableTool(), ts.describeTable)
	add(s, ts, queryTool(), ts.query)
	add(s, ts, topCustomersTool(), ts.topCustomers)
	add(s, ts, topProductsTool(), ts.topProducts)
	add(s, ts, revenueSummaryTool(), ts.revenueSummary)
}

// handler is a tool implementation; rows is only used for logging.
type handler[In, Out any] func(ctx context.Context, in In) (out Out, rows int, err error)

func add[In, Out any](s *mcp.Server, ts *toolset, tool *mcp.Tool, h handler[In, Out]) {
	tool.Annotations = &mcp.ToolAnnotations{
		ReadOnlyHint:   true,
		IdempotentHint: true,
		OpenWorldHint:  new(false),
	}
	name := tool.Name
	mcp.AddTool(s, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		start := time.Now()
		callCtx, cancel := context.WithTimeout(ctx, ts.timeout)
		defer cancel()

		out, rows, err := h(callCtx, in)
		attrs := []any{"tool", name, "duration_ms", time.Since(start).Milliseconds(), "rows", rows}
		if err != nil {
			msg := ts.message(ctx, err)
			ts.log.Info("tool call", append(attrs, "error", causeOf(err).Error())...)
			var zero Out
			return nil, zero, errors.New(msg)
		}
		ts.log.Info("tool call", attrs...)
		return nil, out, nil
	})
}

// prop describes JSON Schema constraints added on top of the inferred schema.
type prop struct {
	enum     []string
	itemEnum []string // for array properties
	def      any
	min, max *float64
	maxLen   *int
}

func num(f float64) *float64 { return &f }

// inputSchema infers the schema of In and applies the constraints in props.
func inputSchema[In any](props map[string]prop) *jsonschema.Schema {
	s, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("infer input schema for %T: %v", *new(In), err))
	}
	for name, p := range props {
		ps, ok := s.Properties[name]
		if !ok {
			panic(fmt.Sprintf("input schema for %T has no property %q", *new(In), name))
		}
		for _, e := range p.enum {
			ps.Enum = append(ps.Enum, e)
		}
		if len(p.itemEnum) > 0 {
			for _, e := range p.itemEnum {
				ps.Items.Enum = append(ps.Items.Enum, e)
			}
		}
		if p.def != nil {
			b, err := json.Marshal(p.def)
			if err != nil {
				panic(err)
			}
			ps.Default = b
		}
		ps.Minimum, ps.Maximum = p.min, p.max
		ps.MaxLength = p.maxLen
	}
	return s
}
