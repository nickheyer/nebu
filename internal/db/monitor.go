package db

import (
	"context"
	"database/sql"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Inserts or replaces a watch with params and known groups
func (d *DB) PutWatch(ctx context.Context, w *v1.Watch) error {
	return d.tx(ctx, func(exec execFn) error {
		id := w.GetId()
		// An upsert on id so duplicate repos trip the index
		if err := exec(`INSERT INTO watches (id, source_id, repo, revision, group_match, auto_pull, slot_id, runtime_id, profile_id, last_commit, checked_at, created_at, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET source_id = excluded.source_id, repo = excluded.repo, revision = excluded.revision, group_match = excluded.group_match, auto_pull = excluded.auto_pull, slot_id = excluded.slot_id, runtime_id = excluded.runtime_id, profile_id = excluded.profile_id, last_commit = excluded.last_commit, checked_at = excluded.checked_at, created_at = excluded.created_at, error = excluded.error`,
			id, w.GetSourceId(), w.GetRepo(), w.GetRevision(), w.GetGroupMatch(), boolCol(w.GetAutoPull()), w.GetSlotId(), w.GetRuntimeId(), w.GetProfileId(), w.GetLastCommit(), timeCol(w.GetCheckedAt()), stamp(w.GetCreatedAt().AsTime()), w.GetError()); err != nil {
			return err
		}
		if err := clearChildren(exec, "watch_id", id, "watch_params", "watch_groups"); err != nil {
			return err
		}
		if err := putMap(exec, `INSERT INTO watch_params (watch_id, name, value) VALUES (?, ?, ?)`, id, w.GetParams()); err != nil {
			return err
		}
		for i, g := range w.GetKnownGroups() {
			if err := exec(`INSERT INTO watch_groups (watch_id, position, group_name) VALUES (?, ?, ?)`, id, i, g); err != nil {
				return err
			}
		}
		return nil
	})
}

// Lists every watch oldest first
func (d *DB) ListWatches(ctx context.Context) ([]*v1.Watch, error) {
	out, err := list(ctx, d, `SELECT id, source_id, repo, revision, group_match, auto_pull, slot_id, runtime_id, profile_id, last_commit, checked_at, created_at, error FROM watches ORDER BY created_at, id`, func(rows *sql.Rows) (*v1.Watch, error) {
		w := &v1.Watch{}
		return w, rows.Scan(&w.Id, &w.SourceId, &w.Repo, &w.Revision, &w.GroupMatch, (*flag)(&w.AutoPull), &w.SlotId, &w.RuntimeId, &w.ProfileId, &w.LastCommit, at{&w.CheckedAt}, at{&w.CreatedAt}, &w.Error)
	})
	if err != nil {
		return nil, err
	}
	for _, w := range out {
		if w.Params, err = d.stringMap(ctx, `SELECT name, value FROM watch_params WHERE watch_id = ? ORDER BY name`, w.GetId()); err != nil {
			return nil, err
		}
		if w.KnownGroups, err = d.strings(ctx, `SELECT group_name FROM watch_groups WHERE watch_id = ? ORDER BY position`, w.GetId()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Removes a watch and its findings, reporting existence
func (d *DB) DeleteWatch(ctx context.Context, id string) (bool, error) {
	if _, err := d.del(ctx, "findings", "watch_id", id); err != nil {
		return false, err
	}
	return d.del(ctx, "watches", "id", id)
}

// Inserts or replaces a want with its params
func (d *DB) PutWant(ctx context.Context, w *v1.Want) error {
	return d.tx(ctx, func(exec execFn) error {
		id := w.GetId()
		if err := exec(`INSERT OR REPLACE INTO wants (id, query, kind, source_id, group_match, format_id, auto_pull, slot_id, runtime_id, profile_id, found_source_id, found_repo, found_group, task_id, satisfied, checked_at, created_at, error, swap_task_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, w.GetQuery(), enumCol(w.GetKind()), w.GetSourceId(), w.GetGroupMatch(), w.GetFormatId(), boolCol(w.GetAutoPull()), w.GetSlotId(), w.GetRuntimeId(), w.GetProfileId(), w.GetFoundSourceId(), w.GetFoundRepo(), w.GetFoundGroup(), w.GetTaskId(), boolCol(w.GetSatisfied()), timeCol(w.GetCheckedAt()), stamp(w.GetCreatedAt().AsTime()), w.GetError(), w.GetSwapTaskId()); err != nil {
			return err
		}
		if err := clearChildren(exec, "want_id", id, "want_params"); err != nil {
			return err
		}
		return putMap(exec, `INSERT INTO want_params (want_id, name, value) VALUES (?, ?, ?)`, id, w.GetParams())
	})
}

// Lists every want oldest first
func (d *DB) ListWants(ctx context.Context) ([]*v1.Want, error) {
	out, err := list(ctx, d, `SELECT id, query, kind, source_id, group_match, format_id, auto_pull, slot_id, runtime_id, profile_id, found_source_id, found_repo, found_group, task_id, satisfied, checked_at, created_at, error, swap_task_id FROM wants ORDER BY created_at, id`, func(rows *sql.Rows) (*v1.Want, error) {
		w := &v1.Want{}
		return w, rows.Scan(&w.Id, &w.Query, enumAt[v1.SourceKind]{&w.Kind}, &w.SourceId, &w.GroupMatch, &w.FormatId, (*flag)(&w.AutoPull), &w.SlotId, &w.RuntimeId, &w.ProfileId, &w.FoundSourceId, &w.FoundRepo, &w.FoundGroup, &w.TaskId, (*flag)(&w.Satisfied), at{&w.CheckedAt}, at{&w.CreatedAt}, &w.Error, &w.SwapTaskId)
	})
	if err != nil {
		return nil, err
	}
	for _, w := range out {
		if w.Params, err = d.stringMap(ctx, `SELECT name, value FROM want_params WHERE want_id = ? ORDER BY name`, w.GetId()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Removes a want and its findings, reporting existence
func (d *DB) DeleteWant(ctx context.Context, id string) (bool, error) {
	if _, err := d.del(ctx, "findings", "want_id", id); err != nil {
		return false, err
	}
	return d.del(ctx, "wants", "id", id)
}

// Inserts or replaces a finding
func (d *DB) PutFinding(ctx context.Context, f *v1.Finding) error {
	_, err := d.sql.ExecContext(ctx, `INSERT OR REPLACE INTO findings (id, watch_id, want_id, source_id, kind, repo, commit_id, group_name, detail, task_id, acknowledged, found_at, swap_task_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.GetId(), f.GetWatchId(), f.GetWantId(), f.GetSourceId(), enumCol(f.GetKind()), f.GetRepo(), f.GetCommit(), f.GetGroup(), f.GetDetail(), f.GetTaskId(), boolCol(f.GetAcknowledged()), stamp(f.GetFoundAt().AsTime()), f.GetSwapTaskId())
	return err
}

// Returns one finding by id
func (d *DB) GetFinding(ctx context.Context, id string) (*v1.Finding, error) {
	items, err := d.findings(ctx, `WHERE id = ?`, id)
	return one(items, err, "finding", id)
}

// Lists findings newest first, narrowed to a watch or a want when named
func (d *DB) ListFindings(ctx context.Context, watchID, wantID string, unackedOnly bool) ([]*v1.Finding, error) {
	var clauses []string
	var args []any
	if watchID != "" {
		clauses, args = append(clauses, `watch_id = ?`), append(args, watchID)
	}
	if wantID != "" {
		clauses, args = append(clauses, `want_id = ?`), append(args, wantID)
	}
	if unackedOnly {
		clauses = append(clauses, `acknowledged = 0`)
	}
	where := ""
	if len(clauses) > 0 {
		where = `WHERE ` + strings.Join(clauses, ` AND `)
	}
	return d.findings(ctx, where, args...)
}

// Drops acknowledged findings past the newest keep, returning what went so the stream can say
func (d *DB) PruneFindings(ctx context.Context, keep int) ([]*v1.Finding, error) {
	gone, err := d.findings(ctx, `WHERE acknowledged = 1 AND id NOT IN (SELECT id FROM findings ORDER BY found_at DESC, id DESC LIMIT ?)`, keep)
	if err != nil {
		return nil, err
	}
	for _, f := range gone {
		if _, err := d.del(ctx, "findings", "id", f.GetId()); err != nil {
			return nil, err
		}
	}
	return gone, nil
}

func (d *DB) findings(ctx context.Context, where string, args ...any) ([]*v1.Finding, error) {
	return list(ctx, d, `SELECT id, watch_id, want_id, source_id, kind, repo, commit_id, group_name, detail, task_id, acknowledged, found_at, swap_task_id FROM findings `+where+` ORDER BY found_at DESC, id DESC`, func(rows *sql.Rows) (*v1.Finding, error) {
		f := &v1.Finding{}
		return f, rows.Scan(&f.Id, &f.WatchId, &f.WantId, &f.SourceId, enumAt[v1.FindingKind]{&f.Kind}, &f.Repo, &f.Commit, &f.Group, &f.Detail, &f.TaskId, (*flag)(&f.Acknowledged), at{&f.FoundAt}, &f.SwapTaskId)
	}, args...)
}
