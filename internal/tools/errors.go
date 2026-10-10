package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"modernc.org/sqlite"

	"github.com/lama2x2/shop-mcp/internal/sqlguard"
)

// userError carries a message meant for the agent and the underlying cause
// meant for the log.
type userError struct {
	msg   string
	cause error
}

func (e *userError) Error() string { return e.msg }
func (e *userError) Unwrap() error { return e.cause }

func userErrorf(cause error, format string, args ...any) error {
	return &userError{msg: fmt.Sprintf(format, args...), cause: cause}
}

func causeOf(err error) error {
	var ue *userError
	if errors.As(err, &ue) && ue.cause != nil {
		return ue.cause
	}
	return err
}

const sqliteHint = "This is SQLite: use strftime('%Y', order_date) for dates, || for concatenation."

var (
	reNoColumn  = regexp.MustCompile(`no such column: (\S+)`)
	reNoTable   = regexp.MustCompile(`no such table: (\S+)`)
	reSyntax    = regexp.MustCompile(`near "((?:[^"]|"")*)": syntax error`)
	reAmbiguous = regexp.MustCompile(`ambiguous column name: (\S+)`)
	reCode      = regexp.MustCompile(`\s*\(\d+\)(\s*\(SQLITE_\w+\))?$`)
	reDelete    = regexp.MustCompile(`(?is)^\s*DELETE\s+FROM\s+(.+?)\s*;?\s*$`)
	reWord      = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
)

// message turns err into one human-readable sentence plus a hint. It never
// includes Go error chains, stack traces or file paths.
func (ts *toolset) message(ctx context.Context, err error) string {
	return ts.queryMessage(ctx, "", err)
}

// queryMessage is message for errors from running sql; the SQL text is used
// to pick the tables whose columns are listed in hints.
func (ts *toolset) queryMessage(ctx context.Context, sql string, err error) string {
	var ue *userError
	if errors.As(err, &ue) {
		return ue.msg
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("Query exceeded the %s time limit. Add filters or aggregate instead of scanning raw rows.", ts.timeout)
	}
	if errors.Is(err, context.Canceled) {
		return "The request was cancelled."
	}

	var ge *sqlguard.Error
	if errors.As(err, &ge) {
		if ge.Keyword != "" {
			return refusal(ge.Keyword, sql)
		}
		return fmt.Sprintf("Rejected: %s. Send exactly one SELECT or WITH … SELECT statement.", ge.Reason)
	}

	var se *sqlite.Error
	if !errors.As(err, &se) {
		return "Query failed: internal error (details are in the server log)."
	}
	msg := reCode.ReplaceAllString(strings.TrimPrefix(se.Error(), "SQL logic error: "), "")

	// Hints query the store; give them their own budget since ctx may be
	// the expired call context.
	hintCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()

	switch {
	case reNoColumn.MatchString(msg):
		col := reNoColumn.FindStringSubmatch(msg)[1]
		return fmt.Sprintf("Column %q does not exist. Available columns — %s.", col, ts.columnsHint(hintCtx, sql))
	case reNoTable.MatchString(msg):
		tbl := reNoTable.FindStringSubmatch(msg)[1]
		return fmt.Sprintf("Table %q does not exist. Available tables: %s.", tbl, ts.tablesHint(hintCtx))
	case reSyntax.MatchString(msg):
		near := strings.ReplaceAll(reSyntax.FindStringSubmatch(msg)[1], `""`, `"`)
		return fmt.Sprintf("SQL syntax error near %q. %s", near, sqliteHint)
	case strings.Contains(msg, "incomplete input"):
		return "SQL syntax error: the statement is incomplete. " + sqliteHint
	case reAmbiguous.MatchString(msg):
		col := reAmbiguous.FindStringSubmatch(msg)[1]
		return fmt.Sprintf("Column %q is ambiguous; qualify it with a table alias.", col)
	case strings.Contains(msg, "readonly") || strings.Contains(msg, "query_only"):
		return "Refused: this server provides read-only access; only a single SELECT or WITH … SELECT query can be run."
	case strings.Contains(msg, "interrupted"):
		return fmt.Sprintf("Query exceeded the %s time limit. Add filters or aggregate instead of scanning raw rows.", ts.timeout)
	}
	return fmt.Sprintf("Query failed: %s.", strings.TrimSuffix(msg, "."))
}

// refusal explains that writes are impossible and, for a plain DELETE,
// shows the SELECT that lists the rows it would have affected.
func refusal(keyword, sql string) string {
	msg := fmt.Sprintf("Refused: this server provides read-only access. %s statements are not allowed; only a single SELECT or WITH … SELECT query can be run.", keyword)
	if m := reDelete.FindStringSubmatch(sql); m != nil && !strings.Contains(strings.ToUpper(m[1]), "RETURNING") && len(m[1]) <= 500 {
		msg += " To see the affected rows use: SELECT * FROM " + m[1] + "."
	}
	return msg
}

func (ts *toolset) tablesHint(ctx context.Context) string {
	names, err := ts.store.TableNames(ctx)
	if err != nil || len(names) == 0 {
		return "call list_tables"
	}
	return strings.Join(names, ", ")
}

// columnsHint lists columns of the tables mentioned in sql, or of all tables
// when none is mentioned.
func (ts *toolset) columnsHint(ctx context.Context, sql string) string {
	names, err := ts.store.TableNames(ctx)
	if err != nil || len(names) == 0 {
		return "call describe_table"
	}
	mentioned := map[string]bool{}
	for _, w := range reWord.FindAllString(sql, -1) {
		mentioned[strings.ToLower(w)] = true
	}
	used := slices.DeleteFunc(slices.Clone(names), func(n string) bool { return !mentioned[strings.ToLower(n)] })
	if len(used) == 0 {
		used = names
	}
	parts := make([]string, 0, len(used))
	for _, t := range used {
		cols, err := ts.store.Columns(ctx, t)
		if err != nil {
			continue
		}
		parts = append(parts, t+": "+strings.Join(cols, ", "))
	}
	if len(parts) == 0 {
		return "call describe_table"
	}
	return strings.Join(parts, "; ")
}
