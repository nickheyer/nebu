package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/proto"
)

// RFC3339 with all nine fraction digits so text order of start times matches time order
const traceStamp = "2006-01-02T15:04:05.000000000Z07:00"

// Which traces a listing returns
type TraceQuery struct {
	// The subject whose traces are listed, ignored when AllOwners is set
	Owner     string
	AllOwners bool
	// One route's traces when set
	Route string
	// The kinds of trace listed, every kind when empty
	Kinds []v1.TraceKind
	// Traces started at or after From and before To, either bound off when zero
	From, To time.Time
	// Newest first, every match when zero
	Limit int
	// Matches skipped before the first answered
	Offset int
}

// Inserts or replaces a trace with its bodies under the subject that sent it
func (d *DB) PutTrace(ctx context.Context, owner string, t *v1.Trace) error {
	if t.GetId() == "" {
		return errors.New("trace has no id")
	}
	if err := t.GetStartedAt().CheckValid(); err != nil {
		return fmt.Errorf("trace %s start time: %w", t.GetId(), err)
	}
	bare := proto.Clone(t).(*v1.Trace)
	bare.Request, bare.UpstreamRequest, bare.Response, bare.Reasoning = "", "", "", ""
	record, err := proto.Marshal(bare)
	if err != nil {
		return err
	}
	_, err = d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO traces (id, owner, route, kind, started_at, record, request, upstream_request, response, reasoning) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.GetId(), owner, t.GetRoute(), int32(t.GetKind()), traceTime(t.GetStartedAt().AsTime()), record, t.GetRequest(), t.GetUpstreamRequest(), t.GetResponse(), t.GetReasoning())
	return err
}

// Traces newest first by started_at without request, upstream_request, response and reasoning bodies
func (d *DB) ListTraces(ctx context.Context, q TraceQuery) ([]*v1.Trace, error) {
	if q.Limit < 0 {
		return nil, fmt.Errorf("trace limit %d is negative", q.Limit)
	}
	var where []string
	var args []any
	if !q.AllOwners {
		where, args = append(where, `owner = ?`), append(args, q.Owner)
	}
	if q.Route != "" {
		where, args = append(where, `route = ?`), append(args, q.Route)
	}
	if len(q.Kinds) > 0 {
		where = append(where, `kind IN (?`+strings.Repeat(`, ?`, len(q.Kinds)-1)+`)`)
		for _, k := range q.Kinds {
			args = append(args, int32(k))
		}
	}
	if !q.From.IsZero() {
		where, args = append(where, `started_at >= ?`), append(args, traceTime(q.From))
	}
	if !q.To.IsZero() {
		where, args = append(where, `started_at < ?`), append(args, traceTime(q.To))
	}
	query := `SELECT id, record FROM traces`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY started_at DESC, id DESC`
	switch {
	case q.Limit > 0:
		query, args = query+` LIMIT ? OFFSET ?`, append(args, q.Limit, q.Offset)
	case q.Offset > 0:
		query, args = query+` LIMIT -1 OFFSET ?`, append(args, q.Offset)
	}
	return list(ctx, d, query, func(rows *sql.Rows) (*v1.Trace, error) {
		var id string
		var record []byte
		if err := rows.Scan(&id, &record); err != nil {
			return nil, err
		}
		return unmarshalTrace(id, record)
	}, args...)
}

// One trace with its bodies, ErrNotFound when absent or when another subject owns it and anyOwner is false
func (d *DB) GetTrace(ctx context.Context, owner string, anyOwner bool, id string) (*v1.Trace, error) {
	query, args := `SELECT record, request, upstream_request, response, reasoning FROM traces WHERE id = ?`, []any{id}
	if !anyOwner {
		query, args = query+` AND owner = ?`, append(args, owner)
	}
	items, err := list(ctx, d, query, func(rows *sql.Rows) (*v1.Trace, error) {
		var record []byte
		var request, upstream, response, reasoning string
		if err := rows.Scan(&record, &request, &upstream, &response, &reasoning); err != nil {
			return nil, err
		}
		t, err := unmarshalTrace(id, record)
		if err != nil {
			return nil, err
		}
		t.Request, t.UpstreamRequest, t.Response, t.Reasoning = request, upstream, response, reasoning
		return t, nil
	}, args...)
	return one(items, err, "trace", id)
}

// Removes traces that started before the cutoff, answering how many went
func (d *DB) PruneTraces(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM traces WHERE started_at < ?`, traceTime(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Formats a start time in UTC at fixed width for the started_at column
func traceTime(t time.Time) string { return t.UTC().Format(traceStamp) }

// Decodes a stored record into a trace, naming the trace when the bytes are corrupt
func unmarshalTrace(id string, record []byte) (*v1.Trace, error) {
	t := &v1.Trace{}
	if err := proto.Unmarshal(record, t); err != nil {
		return nil, fmt.Errorf("trace %s record: %w", id, err)
	}
	return t, nil
}
