package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Inserts or replaces a task and its labels, keeping its logs
func (d *DB) PutTask(ctx context.Context, t *v1.Task) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		p := t.GetProgress()
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks (id, kind, title, state, progress_done, progress_total, progress_message, error, created_at, started_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, title = excluded.title, state = excluded.state, progress_done = excluded.progress_done, progress_total = excluded.progress_total, progress_message = excluded.progress_message, error = excluded.error, created_at = excluded.created_at, started_at = excluded.started_at, finished_at = excluded.finished_at`,
			t.GetId(), t.GetKind(), t.GetTitle(), enumCol(t.GetState()), int64(p.GetDone()), int64(p.GetTotal()), p.GetMessage(), t.GetError(), stamp(t.GetCreatedAt().AsTime()), timeCol(t.GetStartedAt()), timeCol(t.GetFinishedAt())); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_labels WHERE task_id = ?`, t.GetId()); err != nil {
			return err
		}
		return putMap(func(q string, args ...any) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		}, `INSERT INTO task_labels (task_id, key, value) VALUES (?, ?, ?)`, t.GetId(), t.GetLabels())
	})
}

// Appends one log line at an absolute position
func (d *DB) AppendTaskLog(ctx context.Context, id string, position int, line string) error {
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO task_logs (task_id, position, line) VALUES (?, ?, ?)`, id, position, line)
	return err
}

// Returns one task with its logs in order
func (d *DB) GetTask(ctx context.Context, id string) (*v1.Task, []string, error) {
	list, err := d.tasks(ctx, `WHERE id = ?`, 1, id)
	if err != nil {
		return nil, nil, err
	}
	if len(list) == 0 {
		return nil, nil, fmt.Errorf("%w: task %q", ErrNotFound, id)
	}
	logs, err := d.strings(ctx, `SELECT line FROM task_logs WHERE task_id = ? ORDER BY position`, id)
	if err != nil {
		return nil, nil, err
	}
	return list[0], logs, nil
}

// Lists tasks newest first up to limit
func (d *DB) ListTasks(ctx context.Context, limit int) ([]*v1.Task, error) {
	return d.tasks(ctx, ``, limit)
}

// Marks every task still pending or running as failed with reason
func (d *DB) FailUnfinishedTasks(ctx context.Context, reason string, finishedAt time.Time) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `UPDATE tasks SET state = ?, error = ?, finished_at = ? WHERE state IN (?, ?)`,
		enumCol(v1.TaskState_TASK_STATE_FAILED), reason, stamp(finishedAt), enumCol(v1.TaskState_TASK_STATE_PENDING), enumCol(v1.TaskState_TASK_STATE_RUNNING))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Drops the oldest finished tasks beyond keep
func (d *DB) PruneTasks(ctx context.Context, keep int) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM tasks WHERE state NOT IN (?, ?) AND id NOT IN (SELECT id FROM tasks WHERE state NOT IN (?, ?) ORDER BY created_at DESC, id DESC LIMIT ?)`,
		enumCol(v1.TaskState_TASK_STATE_PENDING), enumCol(v1.TaskState_TASK_STATE_RUNNING), enumCol(v1.TaskState_TASK_STATE_PENDING), enumCol(v1.TaskState_TASK_STATE_RUNNING), keep)
	return err
}

func (d *DB) tasks(ctx context.Context, where string, limit int, args ...any) ([]*v1.Task, error) {
	args = append(args, limit)
	rows, err := d.sql.QueryContext(ctx, `SELECT id, kind, title, state, progress_done, progress_total, progress_message, error, created_at, started_at, finished_at FROM tasks `+where+` ORDER BY created_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	var out []*v1.Task
	for rows.Next() {
		t := &v1.Task{Progress: &v1.TaskProgress{}}
		var state, created string
		var started, finished sql.NullString
		var done, total int64
		if err := rows.Scan(&t.Id, &t.Kind, &t.Title, &state, &done, &total, &t.Progress.Message, &t.Error, &created, &started, &finished); err != nil {
			rows.Close()
			return nil, err
		}
		t.State = v1.TaskState(enumVal(v1.TaskState(0).Descriptor(), state))
		t.Progress.Done, t.Progress.Total = uint64(done), uint64(total)
		t.CreatedAt = timeVal(sql.NullString{String: created, Valid: true})
		t.StartedAt = timeVal(started)
		t.FinishedAt = timeVal(finished)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, t := range out {
		labels, err := d.stringMap(ctx, `SELECT key, value FROM task_labels WHERE task_id = ? ORDER BY key`, t.GetId())
		if err != nil {
			return nil, err
		}
		if len(labels) > 0 {
			t.Labels = labels
		}
	}
	return out, nil
}
