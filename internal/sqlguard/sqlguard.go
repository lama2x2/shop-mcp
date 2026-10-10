// Package sqlguard decides whether a SQL text is a single read-only
// SELECT / WITH … SELECT statement. It is a lexical check: comments are
// dropped, string literals and quoted identifiers become opaque tokens, and
// the remaining bare keywords are matched against a deny list.
//
// The guard is one of three independent layers; the SQLite connection itself
// is opened read-only, so a guard miss still cannot modify the database.
package sqlguard

import (
	"errors"
	"fmt"
	"strings"
)

// ErrReadOnly is matched (via errors.Is) by an *Error caused by a write or
// schema-changing keyword.
var ErrReadOnly = errors.New("sqlguard: write statements are not allowed")

// Error explains why a SQL text was rejected.
type Error struct {
	// Reason is a short human-readable explanation, e.g. "multiple statements".
	Reason string
	// Keyword is the denied keyword in upper case (e.g. "DELETE"); empty when
	// the rejection is not caused by a write keyword.
	Keyword string
}

func (e *Error) Error() string {
	if e.Keyword != "" {
		return fmt.Sprintf("sqlguard: %s (%s)", e.Reason, e.Keyword)
	}
	return "sqlguard: " + e.Reason
}

// Is reports whether target is ErrReadOnly and the error was caused by a
// denied keyword.
func (e *Error) Is(target error) bool {
	return target == ErrReadOnly && e.Keyword != ""
}

var denied = map[string]bool{
	"INSERT": true, "UPDATE": true, "DELETE": true, "REPLACE": true, "MERGE": true,
	"UPSERT": true, "DROP": true, "ALTER": true, "CREATE": true, "ATTACH": true,
	"DETACH": true, "PRAGMA": true, "VACUUM": true, "REINDEX": true, "ANALYZE": true,
	"BEGIN": true, "COMMIT": true, "ROLLBACK": true, "SAVEPOINT": true, "RELEASE": true,
	"TRUNCATE": true,
}

var deniedFuncs = map[string]bool{
	"LOAD_EXTENSION": true,
}

// Check returns nil if sql is exactly one SELECT or WITH … SELECT statement
// without write keywords, and an *Error otherwise.
func Check(sql string) error {
	toks, err := lex(sql)
	if err != nil {
		return err
	}
	if len(toks) == 0 {
		return &Error{Reason: "empty query"}
	}

	for i, t := range toks {
		if t.kind == tokSemicolon && i != len(toks)-1 {
			return &Error{Reason: "multiple statements"}
		}
	}
	if toks[len(toks)-1].kind == tokSemicolon {
		toks = toks[:len(toks)-1]
		if len(toks) == 0 {
			return &Error{Reason: "empty query"}
		}
	}

	for i, t := range toks {
		if t.kind != tokWord {
			continue
		}
		w := strings.ToUpper(t.text)
		if deniedFuncs[w] {
			return &Error{Reason: strings.ToLower(w) + "() is not allowed"}
		}
		if !denied[w] {
			continue
		}
		if w == "REPLACE" && i+1 < len(toks) && toks[i+1].kind == tokPunct && toks[i+1].text == "(" {
			continue // scalar function replace(x, y, z)
		}
		return &Error{Reason: "write or schema statements are not allowed", Keyword: w}
	}

	first := toks[0]
	if first.kind != tokWord {
		return &Error{Reason: "query must start with SELECT or WITH"}
	}
	switch strings.ToUpper(first.text) {
	case "SELECT", "WITH":
		return nil
	default:
		return &Error{Reason: fmt.Sprintf("query must start with SELECT or WITH, not %s", strings.ToUpper(first.text))}
	}
}

type tokKind int

const (
	tokWord      tokKind = iota // bare identifier or keyword
	tokOpaque                   // string literal or quoted identifier
	tokNumber                   // numeric literal
	tokPunct                    // any other single character
	tokSemicolon                // ;
)

type token struct {
	kind tokKind
	text string
}

func lex(s string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			end := strings.IndexByte(s[i:], '\n')
			if end < 0 {
				i = len(s)
			} else {
				i += end + 1
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return nil, &Error{Reason: "unterminated comment"}
			}
			i += 2 + end + 2
		case c == '\'' || c == '"' || c == '`':
			end, ok := scanQuoted(s, i, c)
			if !ok {
				return nil, &Error{Reason: "unterminated string or quoted identifier"}
			}
			toks = append(toks, token{kind: tokOpaque, text: s[i:end]})
			i = end
		case c == '[':
			end := strings.IndexByte(s[i+1:], ']')
			if end < 0 {
				return nil, &Error{Reason: "unterminated [identifier]"}
			}
			toks = append(toks, token{kind: tokOpaque, text: s[i : i+1+end+1]})
			i += 1 + end + 1
		case c == ';':
			toks = append(toks, token{kind: tokSemicolon, text: ";"})
			i++
		case isDigit(c) || (c == '.' && i+1 < len(s) && isDigit(s[i+1])):
			j := i + 1
			for j < len(s) && (isWordByte(s[j]) || s[j] == '.') {
				j++
			}
			toks = append(toks, token{kind: tokNumber, text: s[i:j]})
			i = j
		case isWordStart(c):
			j := i + 1
			for j < len(s) && (isWordByte(s[j]) || s[j] == '$') {
				j++
			}
			toks = append(toks, token{kind: tokWord, text: s[i:j]})
			i = j
		default:
			toks = append(toks, token{kind: tokPunct, text: s[i : i+1]})
			i++
		}
	}
	return toks, nil
}

// scanQuoted returns the index just past the closing quote q of the literal
// starting at s[start]; a doubled quote is an escaped quote.
func scanQuoted(s string, start int, q byte) (int, bool) {
	for j := start + 1; j < len(s); j++ {
		if s[j] != q {
			continue
		}
		if j+1 < len(s) && s[j+1] == q {
			j++
			continue
		}
		return j + 1, true
	}
	return 0, false
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isWordStart treats every non-ASCII byte as an identifier character, as
// SQLite does.
func isWordStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isWordByte(c byte) bool { return isWordStart(c) || isDigit(c) }
