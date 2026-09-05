package db

import (
	"context"
	"database/sql"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Inserts or replaces a task and labels, keeping logs
func (d *DB) PutTask(ctx context.Context, t *v1.Task) error {
	return d.tx(ctx, func(exec execFn) error {
		p := t.GetProgress()
		if err := exec(`INSERT INTO tasks (id, kind, title, state, progress_done, progress_total, progress_message, error, created_at, started_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, title = excluded.title, state = excluded.state, progress_done = excluded.progress_done, progress_total = excluded.progress_total, progress_message = excluded.progress_message, error = excluded.error, created_at = excluded.created_at, started_at = excluded.started_at, finished_at = excluded.finished_at`,
			t.GetId(), t.GetKind(), t.GetTitle(), enumCol(t.GetState()), int64(p.GetDone()), int64(p.GetTotal()), p.GetMessage(), t.GetError(), stamp(t.GetCreatedAt().AsTime()), timeCol(t.GetStartedAt()), timeCol(t.GetFinishedAt())); err != nil {
			return err
		}
		if err := clearChildren(exec, "task_id", t.GetId(), "task_labels"); err != nil {
			return err
		}
		return putMap(exec, `INSERT INTO task_labels (task_id, key, value) VALUES (?, ?, ?)`, t.GetId(), t.GetLabels())
	})
}

// Appends one log line at an absolute position
func (d *DB) AppendTaskLog(ctx context.Context, id string, position int, line string) error {
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO task_logs (task_id, position, line) VALUES (?, ?, ?)`, id, position, line)
	return err
}

// Returns one task with its logs in order
func (d *DB) GetTask(ctx context.Context, id string) (*v1.Task, []string, error) {
	items, err := d.tasks(ctx, `WHERE id = ?`, 1, id)
	t, err := one(items, err, "task", id)
	if err != nil {
		return nil, nil, err
	}
	logs, err := d.strings(ctx, `SELECT line FROM task_logs WHERE task_id = ? ORDER BY position`, id)
	return t, logs, err
}

// Lists tasks newest first up to limit
func (d *DB) ListTasks(ctx context.Context, limit int) ([]*v1.Task, error) {
	return d.tasks(ctx, ``, limit)
}

// Marks every pending or running task failed by a restart
func (d *DB) FailUnfinishedTasks(ctx context.Context) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `UPDATE tasks SET state = ?, error = ?, finished_at = ? WHERE state IN (?, ?)`,
		enumCol(v1.TaskState_TASK_STATE_FAILED), RestartNote, stamp(time.Now()), enumCol(v1.TaskState_TASK_STATE_PENDING), enumCol(v1.TaskState_TASK_STATE_RUNNING))
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
	out, err := list(ctx, d, `SELECT id, kind, title, state, progress_done, progress_total, progress_message, error, created_at, started_at, finished_at FROM tasks `+where+` ORDER BY created_at DESC, id DESC LIMIT ?`, func(rows *sql.Rows) (*v1.Task, error) {
		t := &v1.Task{Progress: &v1.TaskProgress{}}
		return t, rows.Scan(&t.Id, &t.Kind, &t.Title, enumAt[v1.TaskState]{&t.State}, &t.Progress.Done, &t.Progress.Total, &t.Progress.Message, &t.Error, at{&t.CreatedAt}, at{&t.StartedAt}, at{&t.FinishedAt})
	}, append(args, limit)...)
	if err != nil {
		return nil, err
	}
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
