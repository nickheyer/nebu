package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/nickheyer/nebu/pkg/eval"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// What a record left unfinished by a previous daemon says when marked failed
const RestartNote = "daemon restarted"

// Runs one statement
type execFn func(query string, args ...any) error

// Runs fn in one transaction, its statements through exec
func (d *DB) tx(ctx context.Context, fn func(exec execFn) error) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	exec := func(query string, args ...any) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	}
	if err := fn(exec); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Reads every row, scan filling one, the cursor closed before the caller queries again
func list[T any](ctx context.Context, d *DB, query string, scan func(*sql.Rows) (T, error), args ...any) ([]T, error) {
	rows, err := d.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// The first item, ErrNotFound naming what was asked when there is none
func one[T any](items []T, err error, what, id string) (T, error) {
	var zero T
	if err != nil {
		return zero, err
	}
	if len(items) == 0 {
		return zero, fmt.Errorf("%w: %s %q", ErrNotFound, what, id)
	}
	return items[0], nil
}

// Deletes rows whose column holds id, reporting whether any went
func (d *DB) del(ctx context.Context, table, column, id string) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+column+` = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Clears the child rows of one parent
func clearChildren(exec execFn, column, id string, tables ...string) error {
	for _, table := range tables {
		if err := exec(`DELETE FROM `+table+` WHERE `+column+` = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) strings(ctx context.Context, query string, args ...any) ([]string, error) {
	return list(ctx, d, query, func(rows *sql.Rows) (s string, err error) { return s, rows.Scan(&s) }, args...)
}

func (d *DB) stringMap(ctx context.Context, query string, args ...any) (map[string]string, error) {
	out := map[string]string{}
	_, err := list(ctx, d, query, func(rows *sql.Rows) (struct{}, error) {
		var k, v string
		err := rows.Scan(&k, &v)
		out[k] = v
		return struct{}{}, err
	}, args...)
	return out, err
}

// Writes map entries as rows in key order
func putMap(exec execFn, query, id string, m map[string]string) error {
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if err := exec(query, id, k, m[k]); err != nil {
			return err
		}
	}
	return nil
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// Stores a timestamp as RFC3339 text, NULL when unset
func timeCol(ts *timestamppb.Timestamp) sql.NullString {
	if ts == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: stamp(ts.AsTime()), Valid: true}
}

// Scans a timestamp column into the field, nil when NULL or unreadable
type at struct{ p **timestamppb.Timestamp }

func (a at) Scan(v any) error {
	*a.p = nil
	s, ok := v.(string)
	if !ok {
		return nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		*a.p = timestamppb.New(t)
	}
	return nil
}

// Scans a short enum name back into the field, zero when unknown
type enumAt[E ~int32] struct{ p *E }

func (e enumAt[E]) Scan(v any) error {
	s, _ := v.(string)
	*e.p = enumOf[E](s)
	return nil
}

// Scans a 0 or 1 column into a bool field
type flag bool

func (f *flag) Scan(v any) error {
	n, _ := v.(int64)
	*f = n != 0
	return nil
}

// Stores an enum as its short lower case name
func enumCol(e protoreflect.Enum) string { return eval.EnumShort(e) }

// Reads a short enum name back, zero when unknown
func enumOf[E ~int32](short string) E {
	var zero E
	d := any(zero).(protoreflect.Enum).Descriptor()
	prefix := screaming(string(d.Name())) + "_"
	want := strings.ToUpper(short)
	values := d.Values()
	for i := 0; i < values.Len(); i++ {
		if v := values.Get(i); strings.TrimPrefix(string(v.Name()), prefix) == want {
			return E(v.Number())
		}
	}
	return zero
}

// Converts CamelCase to SCREAMING_SNAKE the same way eval.EnumShort strips it
func screaming(s string) string {
	var b strings.Builder
	prev := rune(0)
	for i, r := range s {
		if i > 0 && unicode.IsUpper(r) && unicode.IsLower(prev) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToUpper(r))
		prev = r
	}
	return b.String()
}

func boolCol(b bool) int {
	if b {
		return 1
	}
	return 0
}

// A fresh row id, twelve hex characters of randomness
func NewID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%012x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
