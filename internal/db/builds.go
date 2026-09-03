package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Inserts or replaces a build with vars, facts, and patches
func (d *DB) PutBuild(ctx context.Context, b *v1.Build) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		exec := func(q string, args ...any) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		}
		id := b.GetId()
		if err := exec(`INSERT OR REPLACE INTO builds (id, recipe_id, runtime_id, variant, ref, commit_id, sandbox, image, dir, binary, install_id, task_id, state, error, created_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, b.GetRecipeId(), b.GetRuntimeId(), b.GetVariant(), b.GetRef(), b.GetCommit(), enumCol(b.GetSandbox()), b.GetImage(), b.GetDir(), b.GetBinary(), b.GetInstallId(), b.GetTaskId(), enumCol(b.GetState()), b.GetError(), stamp(b.GetCreatedAt().AsTime()), timeCol(b.GetFinishedAt())); err != nil {
			return err
		}
		for _, table := range []string{"build_vars", "build_facts", "build_patches"} {
			if err := exec(`DELETE FROM `+table+` WHERE build_id = ?`, id); err != nil {
				return err
			}
		}
		if err := putMap(exec, `INSERT INTO build_vars (build_id, name, value) VALUES (?, ?, ?)`, id, b.GetVars()); err != nil {
			return err
		}
		if err := putMap(exec, `INSERT INTO build_facts (build_id, key, value) VALUES (?, ?, ?)`, id, b.GetFacts()); err != nil {
			return err
		}
		for i, p := range b.GetPatches() {
			if err := exec(`INSERT INTO build_patches (build_id, position, patch_id) VALUES (?, ?, ?)`, id, i, p); err != nil {
				return err
			}
		}
		return nil
	})
}

// Returns one build by id
func (d *DB) GetBuild(ctx context.Context, id string) (*v1.Build, error) {
	list, err := d.builds(ctx, `WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%w: build %q", ErrNotFound, id)
	}
	return list[0], nil
}

// Lists builds newest first, all runtimes when runtimeID is empty
func (d *DB) ListBuilds(ctx context.Context, runtimeID string) ([]*v1.Build, error) {
	if runtimeID == "" {
		return d.builds(ctx, ``)
	}
	return d.builds(ctx, `WHERE runtime_id = ?`, runtimeID)
}

// Removes a build, reporting whether it existed
func (d *DB) DeleteBuild(ctx context.Context, id string) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM builds WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Marks every build still running as failed with reason
func (d *DB) FailUnfinishedBuilds(ctx context.Context, reason string, finishedAt time.Time) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `UPDATE builds SET state = ?, error = ?, finished_at = ? WHERE state = ?`,
		enumCol(v1.BuildState_BUILD_STATE_FAILED), reason, stamp(finishedAt), enumCol(v1.BuildState_BUILD_STATE_RUNNING))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (d *DB) builds(ctx context.Context, where string, args ...any) ([]*v1.Build, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT id, recipe_id, runtime_id, variant, ref, commit_id, sandbox, image, dir, binary, install_id, task_id, state, error, created_at, finished_at FROM builds `+where+` ORDER BY created_at DESC, id`, args...)
	if err != nil {
		return nil, err
	}
	var out []*v1.Build
	for rows.Next() {
		b := &v1.Build{}
		var sandbox, state, created string
		var finished sql.NullString
		if err := rows.Scan(&b.Id, &b.RecipeId, &b.RuntimeId, &b.Variant, &b.Ref, &b.Commit, &sandbox, &b.Image, &b.Dir, &b.Binary, &b.InstallId, &b.TaskId, &state, &b.Error, &created, &finished); err != nil {
			rows.Close()
			return nil, err
		}
		b.Sandbox = v1.SandboxKind(enumVal(v1.SandboxKind(0).Descriptor(), sandbox))
		b.State = v1.BuildState(enumVal(v1.BuildState(0).Descriptor(), state))
		b.CreatedAt = timeVal(sql.NullString{String: created, Valid: true})
		b.FinishedAt = timeVal(finished)
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, b := range out {
		if b.Vars, err = d.stringMap(ctx, `SELECT name, value FROM build_vars WHERE build_id = ? ORDER BY name`, b.GetId()); err != nil {
			return nil, err
		}
		if b.Facts, err = d.stringMap(ctx, `SELECT key, value FROM build_facts WHERE build_id = ? ORDER BY key`, b.GetId()); err != nil {
			return nil, err
		}
		if b.Patches, err = d.strings(ctx, `SELECT patch_id FROM build_patches WHERE build_id = ? ORDER BY position`, b.GetId()); err != nil {
			return nil, err
		}
	}
	return out, nil
}
