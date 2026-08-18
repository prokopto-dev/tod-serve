package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// GooseVersionTable is the bookkeeping table the migration runner owns. It is named here so that
// the schema gates can exclude exactly one table by name and say why, rather than carrying a
// general "tables we do not check" list that a future table could quietly join.
const GooseVersionTable = "goose_db_version"

// TableInfo is one table in the applied schema.
type TableInfo struct {
	// Name is the table name.
	Name string
	// Strict reports whether the table was declared STRICT.
	Strict bool
	// WithoutRowID reports whether the table was declared WITHOUT ROWID.
	WithoutRowID bool
	// DDL is the CREATE TABLE statement SQLite stored, which is where the CHECK constraints are.
	DDL string
}

// ColumnInfo is one column of an applied table.
type ColumnInfo struct {
	// Name is the column name.
	Name string
	// Type is the declared type, as written in the DDL.
	Type string
	// NotNull reports whether the column carries NOT NULL.
	NotNull bool
	// PrimaryKeyPosition is 0 for a column outside the primary key, and its 1-based position
	// within the key otherwise.
	PrimaryKeyPosition int
}

// ForeignKeyInfo is one foreign-key column reference.
type ForeignKeyInfo struct {
	// Column is the referring column on this table.
	Column string
	// RefTable is the table referred to.
	RefTable string
	// RefColumn is the column referred to. SQLite reports it empty when the reference is to the
	// parent's primary key by omission; it is filled in here so a caller never has to.
	RefColumn string
}

// TriggerInfo is one trigger in the applied schema.
type TriggerInfo struct {
	// Name is the trigger name.
	Name string
	// Table is the table it fires on.
	Table string
	// DDL is the CREATE TRIGGER statement SQLite stored.
	DDL string
}

// Tables returns every table this project owns, in name order.
//
// SQLite's own internal tables (`sqlite_%`, including the sqlite_sequence AUTOINCREMENT creates)
// and the goose bookkeeping table are excluded: they are not ours, they are not STRICT, and they
// have no tenancy. Excluding them by name here means the gates that walk this list do not each
// carry their own idea of what to skip.
func (d *DB) Tables(ctx context.Context) ([]TableInfo, error) {
	if d.sql == nil {
		return nil, ErrClosed
	}
	ddl, err := d.objectDDL(ctx, "table")
	if err != nil {
		return nil, err
	}

	rows, err := d.sql.QueryContext(ctx, "PRAGMA table_list")
	if err != nil {
		return nil, fmt.Errorf("list tables in %s: %w", d.path, err)
	}
	defer closeRows(rows)

	var out []TableInfo
	for rows.Next() {
		var (
			schema, name, kind string
			ncol, withoutRowID int
			strict             int
		)
		if err := rows.Scan(&schema, &name, &kind, &ncol, &withoutRowID, &strict); err != nil {
			return nil, fmt.Errorf("list tables in %s: %w", d.path, err)
		}
		if schema != "main" || kind != "table" ||
			strings.HasPrefix(name, "sqlite_") || name == GooseVersionTable {
			continue
		}
		out = append(out, TableInfo{
			Name:         name,
			Strict:       strict == 1,
			WithoutRowID: withoutRowID == 1,
			DDL:          ddl[name],
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tables in %s: %w", d.path, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Columns returns the columns of table, in declaration order.
func (d *DB) Columns(ctx context.Context, table string) ([]ColumnInfo, error) {
	if d.sql == nil {
		return nil, ErrClosed
	}
	// PRAGMA table_info takes no bind parameter, so the table name is interpolated. Every caller
	// passes a name that came out of [DB.Tables], which came out of SQLite itself; this is not a
	// place to accept a name from a request, and there is no code path that does.
	rows, err := d.sql.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		return nil, fmt.Errorf("read columns of %s: %w", table, err)
	}
	defer closeRows(rows)

	var out []ColumnInfo
	for rows.Next() {
		var (
			cid      int
			name     string
			declared string
			notNull  int
			deflt    sql.NullString
			pk       int
		)
		if err := rows.Scan(&cid, &name, &declared, &notNull, &deflt, &pk); err != nil {
			return nil, fmt.Errorf("read columns of %s: %w", table, err)
		}
		out = append(out, ColumnInfo{
			Name:               name,
			Type:               declared,
			NotNull:            notNull == 1,
			PrimaryKeyPosition: pk,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read columns of %s: %w", table, err)
	}
	return out, nil
}

// ForeignKeys returns the foreign-key references declared on table.
func (d *DB) ForeignKeys(ctx context.Context, table string) ([]ForeignKeyInfo, error) {
	if d.sql == nil {
		return nil, ErrClosed
	}
	rows, err := d.sql.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_list(%q)", table))
	if err != nil {
		return nil, fmt.Errorf("read foreign keys of %s: %w", table, err)
	}
	defer closeRows(rows)

	var out []ForeignKeyInfo
	for rows.Next() {
		var (
			id, seq                   int
			refTable, from            string
			to                        sql.NullString
			onUpdate, onDelete, match string
		)
		if err := rows.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return nil, fmt.Errorf("read foreign keys of %s: %w", table, err)
		}
		ref := to.String
		if ref == "" {
			// SQLite reports the referenced column as NULL when the DDL omitted it, meaning the
			// parent's primary key. Resolving it here keeps every caller from having to know that.
			ref, err = d.primaryKeyColumn(ctx, refTable)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, ForeignKeyInfo{Column: from, RefTable: refTable, RefColumn: ref})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read foreign keys of %s: %w", table, err)
	}
	return out, nil
}

// Triggers returns every trigger in the applied schema, in name order.
func (d *DB) Triggers(ctx context.Context) ([]TriggerInfo, error) {
	if d.sql == nil {
		return nil, ErrClosed
	}
	rows, err := d.sql.QueryContext(ctx,
		"SELECT name, tbl_name, sql FROM sqlite_master WHERE type = 'trigger' ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list triggers in %s: %w", d.path, err)
	}
	defer closeRows(rows)

	var out []TriggerInfo
	for rows.Next() {
		var t TriggerInfo
		if err := rows.Scan(&t.Name, &t.Table, &t.DDL); err != nil {
			return nil, fmt.Errorf("list triggers in %s: %w", d.path, err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list triggers in %s: %w", d.path, err)
	}
	return out, nil
}

// objectDDL reads the stored CREATE statements for one kind of schema object.
func (d *DB) objectDDL(ctx context.Context, kind string) (map[string]string, error) {
	rows, err := d.sql.QueryContext(ctx,
		"SELECT name, COALESCE(sql, '') FROM sqlite_master WHERE type = ?", kind)
	if err != nil {
		return nil, fmt.Errorf("read %s definitions in %s: %w", kind, d.path, err)
	}
	defer closeRows(rows)

	out := map[string]string{}
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			return nil, fmt.Errorf("read %s definitions in %s: %w", kind, d.path, err)
		}
		out[name] = ddl
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s definitions in %s: %w", kind, d.path, err)
	}
	return out, nil
}

// primaryKeyColumn returns the single-column primary key of table, for resolving a foreign key
// that named no target column.
func (d *DB) primaryKeyColumn(ctx context.Context, table string) (string, error) {
	columns, err := d.Columns(ctx, table)
	if err != nil {
		return "", err
	}
	for _, c := range columns {
		if c.PrimaryKeyPosition == 1 {
			return c.Name, nil
		}
	}
	return "", fmt.Errorf("read primary key of %s: table has none", table)
}
