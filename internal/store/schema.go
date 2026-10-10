package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
)

// Table is a table name with its row count.
type Table struct {
	Name     string
	RowCount int64
}

// Column describes one column of a table.
type Column struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	NotNull    bool   `json:"not_null"`
	PrimaryKey bool   `json:"primary_key"`
	Default    any    `json:"default"`
}

// ForeignKey is an outgoing reference from a column of a table.
type ForeignKey struct {
	Column           string `json:"column"`
	ReferencesTable  string `json:"references_table"`
	ReferencesColumn string `json:"references_column"`
	OnDelete         string `json:"on_delete"`
}

// Reference is an incoming reference to a table.
type Reference struct {
	Table  string `json:"table"`
	Column string `json:"column"`
}

// TableDescription is the full description of one table.
type TableDescription struct {
	Name         string
	RowCount     int64
	Columns      []Column
	ForeignKeys  []ForeignKey
	ReferencedBy []Reference
	SampleRows   []map[string]any
}

// ErrNoSuchTable is returned by Describe and Columns for unknown tables.
var ErrNoSuchTable = errors.New("no such table")

// TableNames returns user tables in creation order.
func (s *Store) TableNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY rowid")
	if err != nil {
		return nil, wrapCtx(ctx, "list tables", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("scan table name: %w", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapCtx(ctx, "list tables", err)
	}
	return names, nil
}

// Tables returns user tables with row counts.
func (s *Store) Tables(ctx context.Context) ([]Table, error) {
	names, err := s.TableNames(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Table, 0, len(names))
	for _, n := range names {
		c, err := s.count(ctx, n)
		if err != nil {
			return nil, err
		}
		out = append(out, Table{Name: n, RowCount: c})
	}
	return out, nil
}

func (s *Store) count(ctx context.Context, table string) (int64, error) {
	var c int64
	// table comes from sqlite_master or was validated against it.
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdent(table)).Scan(&c); err != nil { //nolint:gosec // validated identifier
		return 0, wrapCtx(ctx, "count "+table, err)
	}
	return c, nil
}

func (s *Store) validTable(ctx context.Context, table string) error {
	names, err := s.TableNames(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(names, table) {
		return fmt.Errorf("%w: %s", ErrNoSuchTable, table)
	}
	return nil
}

// Columns returns the column names of table.
func (s *Store) Columns(ctx context.Context, table string) ([]string, error) {
	cols, err := s.columns(ctx, table)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	return names, nil
}

func (s *Store) columns(ctx context.Context, table string) ([]Column, error) {
	if err := s.validTable(ctx, table); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, wrapCtx(ctx, "table info", err)
	}
	defer rows.Close()
	var cols []Column
	for rows.Next() {
		var (
			c   Column
			dft sql.NullString
			pk  int
		)
		if err := rows.Scan(&c.Name, &c.Type, &c.NotNull, &dft, &pk); err != nil {
			return nil, fmt.Errorf("scan column: %w", err)
		}
		c.PrimaryKey = pk > 0
		if dft.Valid {
			c.Default = dft.String
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapCtx(ctx, "table info", err)
	}
	return cols, nil
}

func (s *Store) foreignKeys(ctx context.Context, table string) ([]ForeignKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT "from", "table", "to", on_delete FROM pragma_foreign_key_list(?) ORDER BY id, seq`, table)
	if err != nil {
		return nil, wrapCtx(ctx, "foreign keys", err)
	}
	defer rows.Close()
	fks := []ForeignKey{}
	for rows.Next() {
		var (
			fk ForeignKey
			to sql.NullString
		)
		if err := rows.Scan(&fk.Column, &fk.ReferencesTable, &to, &fk.OnDelete); err != nil {
			return nil, fmt.Errorf("scan foreign key: %w", err)
		}
		fk.ReferencesColumn = to.String
		fks = append(fks, fk)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapCtx(ctx, "foreign keys", err)
	}
	return fks, nil
}

// Relationship is a foreign key edge between two tables.
type Relationship struct {
	Table string
	ForeignKey
}

// Relationships returns all foreign keys between user tables.
func (s *Store) Relationships(ctx context.Context) ([]Relationship, error) {
	names, err := s.TableNames(ctx)
	if err != nil {
		return nil, err
	}
	var out []Relationship
	for _, n := range names {
		fks, err := s.foreignKeys(ctx, n)
		if err != nil {
			return nil, err
		}
		for _, fk := range fks {
			out = append(out, Relationship{Table: n, ForeignKey: fk})
		}
	}
	return out, nil
}

// Describe returns columns, keys, references and up to 3 sample rows of table.
func (s *Store) Describe(ctx context.Context, table string) (*TableDescription, error) {
	cols, err := s.columns(ctx, table)
	if err != nil {
		return nil, err
	}
	d := &TableDescription{Name: table, Columns: cols}
	if d.RowCount, err = s.count(ctx, table); err != nil {
		return nil, err
	}
	if d.ForeignKeys, err = s.foreignKeys(ctx, table); err != nil {
		return nil, err
	}
	rels, err := s.Relationships(ctx)
	if err != nil {
		return nil, err
	}
	d.ReferencedBy = []Reference{}
	for _, r := range rels {
		if r.ReferencesTable == table {
			d.ReferencedBy = append(d.ReferencedBy, Reference{Table: r.Table, Column: r.Column})
		}
	}
	if d.SampleRows, err = s.sampleRows(ctx, table); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Store) sampleRows(ctx context.Context, table string) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT * FROM "+quoteIdent(table)+" ORDER BY rowid LIMIT 3") //nolint:gosec // validated identifier
	if err != nil {
		return nil, wrapCtx(ctx, "sample rows", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}
	out := []map[string]any{}
	for rows.Next() {
		vals, err := scanRow(rows, len(cols))
		if err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapCtx(ctx, "sample rows", err)
	}
	return out, nil
}
