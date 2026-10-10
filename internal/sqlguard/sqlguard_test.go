package sqlguard

import (
	"errors"
	"testing"
)

func TestCheckAllows(t *testing.T) {
	cases := []string{
		"SELECT 1",
		"select * from orders;",
		"WITH t AS (SELECT 1) SELECT * FROM t",
		"SELECT 'DROP TABLE x'",
		`SELECT 1 AS "delete"`,
		"SELECT 1 AS `update`",
		"SELECT 1 AS [insert]",
		"SELECT replace(name,'a','b') FROM products",
		"SELECT REPLACE (name, 'a', 'b') FROM products",
		"SELECT 1 -- delete\n",
		"SELECT 1 -- delete",
		"SELECT /* drop */ 1",
		"SELECT 'it''s; DELETE'",
		"  \n\tSELECT 1  ;  ",
		"SELECT updated_at, created_at, deleted FROM t",
		"SELECT * FROM orders WHERE note = 'DROP TABLE'",
		"SELECT name FROM products WHERE category = 'Электроника'",
		"SELECT 1.5e3, .5, x'00'",
		"WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM r WHERE n < 10) SELECT * FROM r",
	}
	for _, sql := range cases {
		if err := Check(sql); err != nil {
			t.Errorf("Check(%q) = %v, want nil", sql, err)
		}
	}
}

func TestCheckDenies(t *testing.T) {
	cases := []struct {
		sql      string
		readOnly bool // errors.Is(err, ErrReadOnly)
		keyword  string
	}{
		{"SELECT 1; DROP TABLE x", false, ""},
		{"/* */ DELETE FROM orders", true, "DELETE"},
		{"delete from orders", true, "DELETE"},
		{"DeLeTe FROM orders", true, "DELETE"},
		{"WITH x AS (DELETE FROM orders RETURNING *) SELECT * FROM x", true, "DELETE"},
		{"WITH x AS (SELECT 1) DELETE FROM orders", true, "DELETE"},
		{"WITH x AS (SELECT 1) REPLACE INTO t VALUES(1)", true, "REPLACE"},
		{"INSERT INTO t VALUES (1)", true, "INSERT"},
		{"UPDATE orders SET status='x'", true, "UPDATE"},
		{"PRAGMA query_only=OFF", true, "PRAGMA"},
		{"ATTACH 'x.db' AS y", true, "ATTACH"},
		{"VACUUM", true, "VACUUM"},
		{"SELECT 1 FROM t; BEGIN", false, ""},
		{"SELECT load_extension('x')", false, ""},
		{"SELECT 1;;", false, ""},
		{"SELECT 'unterminated", false, ""},
		{`SELECT "unterminated`, false, ""},
		{"SELECT [unterminated", false, ""},
		{"SELECT /* unterminated", false, ""},
		{"", false, ""},
		{"   \n\t ", false, ""},
		{";", false, ""},
		{"-- only a comment", false, ""},
		{"VALUES (1)", false, ""},
		{"EXPLAIN SELECT 1", false, ""},
		{"(SELECT 1)", false, ""},
	}
	for _, c := range cases {
		err := Check(c.sql)
		if err == nil {
			t.Errorf("Check(%q) = nil, want error", c.sql)
			continue
		}
		var gerr *Error
		if !errors.As(err, &gerr) {
			t.Errorf("Check(%q) error %T is not *Error", c.sql, err)
			continue
		}
		if got := errors.Is(err, ErrReadOnly); got != c.readOnly {
			t.Errorf("Check(%q): errors.Is(ErrReadOnly) = %v, want %v (err: %v)", c.sql, got, c.readOnly, err)
		}
		if gerr.Keyword != c.keyword {
			t.Errorf("Check(%q): keyword = %q, want %q", c.sql, gerr.Keyword, c.keyword)
		}
	}
}
