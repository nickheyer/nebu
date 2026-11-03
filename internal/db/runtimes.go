package db

import (
	"context"
	"database/sql"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// A status message as stored, empty text for one never set
func statusCol(m proto.Message) (string, error) {
	if m == nil || !m.ProtoReflect().IsValid() {
		return "", nil
	}
	data, err := protojson.Marshal(m)
	return string(data), err
}

// Inserts or replaces an install and its facts
func (d *DB) PutInstall(ctx context.Context, in *v1.Install) error {
	return d.tx(ctx, func(exec execFn) error {
		id := in.GetId()
		status, err := statusCol(in.GetStatus())
		if err != nil {
			return err
		}
		if err := exec(`INSERT OR REPLACE INTO installs (id, runtime_id, kind, path, dir, version, origin, created_at, build_id, status, generation) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.GetRuntimeId(), enumCol(in.GetKind()), in.GetPath(), in.GetDir(), in.GetVersion(), in.GetOrigin(), stamp(in.GetCreatedAt().AsTime()), in.GetBuildId(), status, int64(in.GetGeneration())); err != nil {
			return err
		}
		if err := clearChildren(exec, "install_id", id, "install_facts"); err != nil {
			return err
		}
		return putMap(exec, `INSERT INTO install_facts (install_id, key, value) VALUES (?, ?, ?)`, id, in.GetFacts())
	})
}

// Returns one install by id
func (d *DB) GetInstall(ctx context.Context, id string) (*v1.Install, error) {
	items, err := d.installs(ctx, `WHERE id = ?`, id)
	return one(items, err, "install", id)
}

// Lists installs newest first, all runtimes when runtimeID is empty
func (d *DB) ListInstalls(ctx context.Context, runtimeID string) ([]*v1.Install, error) {
	if runtimeID == "" {
		return d.installs(ctx, ``)
	}
	return d.installs(ctx, `WHERE runtime_id = ?`, runtimeID)
}

// Removes an install, reporting whether it existed
func (d *DB) DeleteInstall(ctx context.Context, id string) (bool, error) {
	return d.del(ctx, "installs", "id", id)
}

func (d *DB) installs(ctx context.Context, where string, args ...any) ([]*v1.Install, error) {
	out, err := list(ctx, d, `SELECT id, runtime_id, kind, path, dir, version, origin, created_at, build_id, status, generation FROM installs `+where+` ORDER BY created_at DESC, id`, func(rows *sql.Rows) (*v1.Install, error) {
		in := &v1.Install{}
		var status string
		var generation int64
		if err := rows.Scan(&in.Id, &in.RuntimeId, enumAt[v1.InstallKind]{&in.Kind}, &in.Path, &in.Dir, &in.Version, &in.Origin, at{&in.CreatedAt}, &in.BuildId, &status, &generation); err != nil {
			return nil, err
		}
		in.Generation = uint64(generation)
		if status != "" {
			in.Status = &v1.InstallStatus{}
			if err := unmarshalJSON(status, in.Status); err != nil {
				return nil, err
			}
		}
		return in, nil
	}, args...)
	if err != nil {
		return nil, err
	}
	for _, in := range out {
		if in.Facts, err = d.stringMap(ctx, `SELECT key, value FROM install_facts WHERE install_id = ? ORDER BY key`, in.GetId()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Inserts or replaces a build with vars, facts, and patches
func (d *DB) PutBuild(ctx context.Context, b *v1.Build) error {
	return d.tx(ctx, func(exec execFn) error {
		id := b.GetId()
		if err := exec(`INSERT OR REPLACE INTO builds (id, recipe_id, runtime_id, variant, ref, commit_id, sandbox, image, dir, binary, install_id, task_id, state, error, created_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, b.GetRecipeId(), b.GetRuntimeId(), b.GetVariant(), b.GetRef(), b.GetCommit(), enumCol(b.GetSandbox()), b.GetImage(), b.GetDir(), b.GetBinary(), b.GetInstallId(), b.GetTaskId(), enumCol(b.GetState()), b.GetError(), stamp(b.GetCreatedAt().AsTime()), timeCol(b.GetFinishedAt())); err != nil {
			return err
		}
		if err := clearChildren(exec, "build_id", id, "build_vars", "build_facts", "build_patches"); err != nil {
			return err
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
	items, err := d.builds(ctx, `WHERE id = ?`, id)
	return one(items, err, "build", id)
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
	return d.del(ctx, "builds", "id", id)
}

// Marks every build still running as failed by a restart
func (d *DB) FailUnfinishedBuilds(ctx context.Context) (int64, error) {
	res, err := d.sql.ExecContext(ctx, `UPDATE builds SET state = ?, error = ?, finished_at = ? WHERE state = ?`,
		enumCol(v1.BuildState_BUILD_STATE_FAILED), RestartNote, stamp(time.Now()), enumCol(v1.BuildState_BUILD_STATE_RUNNING))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (d *DB) builds(ctx context.Context, where string, args ...any) ([]*v1.Build, error) {
	out, err := list(ctx, d, `SELECT id, recipe_id, runtime_id, variant, ref, commit_id, sandbox, image, dir, binary, install_id, task_id, state, error, created_at, finished_at FROM builds `+where+` ORDER BY created_at DESC, id`, func(rows *sql.Rows) (*v1.Build, error) {
		b := &v1.Build{}
		return b, rows.Scan(&b.Id, &b.RecipeId, &b.RuntimeId, &b.Variant, &b.Ref, &b.Commit, enumAt[v1.SandboxKind]{&b.Sandbox}, &b.Image, &b.Dir, &b.Binary, &b.InstallId, &b.TaskId, enumAt[v1.BuildState]{&b.State}, &b.Error, at{&b.CreatedAt}, at{&b.FinishedAt})
	}, args...)
	if err != nil {
		return nil, err
	}
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
