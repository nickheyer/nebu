package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/pkg/estimate"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/text"
)

func runPull(ctx context.Context, e *env, args []string) error {
	fs := e.flags("pull")
	source := fs.String("source", "", "source id (default: first configured)")
	group := fs.String("group", "", "weight group (required if the repo has several)")
	alone := fs.Bool("alone", false, "download only this group")
	detach := fs.Bool("detach", false, "start the task and return its id")
	to := fs.String("to", "", "pull onto another mesh member, by name or id")
	positional, err := e.parse(fs, args, 1, 1, "pull <repo>[@revision] [flags]")
	if err != nil {
		return err
	}
	if *detach {
		if err := e.requireDaemon(); err != nil {
			return err
		}
	}
	repo, revision := splitRef(positional[0])
	resp, err := e.cl.store.Pull(ctx, connect.NewRequest(&v1.PullRequest{SourceId: *source, Repo: repo, Revision: revision, Group: *group, Alone: *alone, NodeId: *to}))
	if err != nil {
		return err
	}
	task := resp.Msg.GetTask()
	if *detach {
		return e.print(task, func(w io.Writer) { fmt.Fprintln(w, task.GetId()) })
	}
	return e.done(e.follow(ctx, task.GetId()))
}

// Lists a group's parts and their sources.
func runParts(ctx context.Context, e *env, args []string) error {
	fs := e.flags("parts")
	source := fs.String("source", "", "source id (default: first configured)")
	group := fs.String("group", "", "weight group (required if the repo has several)")
	positional, err := e.parse(fs, args, 1, 1, "parts <repo>[@revision] [flags]")
	if err != nil {
		return err
	}
	repo, revision := splitRef(positional[0])
	resp, err := e.cl.estimate.Parts(ctx, connect.NewRequest(&v1.PartsRequest{SourceId: *source, Repo: repo, Revision: revision, Group: *group}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		if resp.Msg.GetFamily() == "" {
			if resp.Msg.GetDescriptor_().GetKind() == v1.ModelKind_MODEL_KIND_COMPONENT {
				fmt.Fprintln(w, "requires a separate diffusion model")
				return
			}
			fmt.Fprintln(w, "component")
			return
		}
		fmt.Fprintf(w, "%s (%s)\n", resp.Msg.GetFamilyName(), resp.Msg.GetFamily())
		var rows [][]string
		for _, part := range resp.Msg.GetParts() {
			where, size := "", "-"
			switch {
			case part.GetError() != "":
				where = "none: " + part.GetError()
			case part.GetBundled():
				where = "in the group"
				if part.GetSubfolder() != "" {
					where += " (" + part.GetSubfolder() + ")"
					size = estimate.Human(part.GetSizeBytes())
				}
			default:
				where = part.GetRepo() + " " + part.GetGroup()
				if len(part.GetPaths()) > 0 {
					where += " (" + strings.Join(part.GetPaths(), ", ") + ")"
				}
				size = estimate.Human(part.GetSizeBytes())
			}
			state := "pull"
			switch {
			case part.GetStored():
				state = "stored"
			case part.GetBundled():
				state = "bundled"
			case part.GetError() != "":
				state = "missing"
			}
			required := "optional"
			if part.GetRequired() {
				required = "required"
			}
			rows = append(rows, []string{part.GetSlot(), part.GetName(), required, state, size, where})
		}
		table(w, []string{"SLOT", "PART", "NEEDED", "STATE", "SIZE", "FROM"}, rows)
		fmt.Fprintf(w, "download size: %s\n", estimate.Human(resp.Msg.GetPullBytes()))
	})
}

func runList(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("list"), args, 0, 0, "list"); err != nil {
		return err
	}
	resp, err := e.cl.store.ListModels(ctx, connect.NewRequest(&v1.ListModelsRequest{}))
	if err != nil {
		return err
	}
	names, err := e.runtimeNames(ctx)
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, m := range resp.Msg.GetModels() {
			// Eviction uses last run time, or pull time for models never run.
			rows = append(rows, []string{m.GetSourceId(), m.GetRepo(), m.GetGroup(), m.GetFormatId(), m.GetDescriptor_().GetArchitecture(), kindWord(m.GetDescriptor_().GetKind()), names.of(m.GetRuntimes()), estimate.Human(m.GetBytes()), when(m.GetPulledAt(), time.DateTime), when(cmp.Or(m.GetUsedAt(), m.GetPulledAt()), time.DateTime), m.GetPath()})
		}
		table(w, []string{"SOURCE", "REPO", "GROUP", "FORMAT", "ARCH", "KIND", "RUNS ON", "SIZE", "PULLED", "USED", "PATH"}, rows)
	})
}

func runRemove(ctx context.Context, e *env, args []string) error {
	fs := e.flags("remove")
	source := fs.String("source", "", "source id (default: first configured)")
	group := fs.String("group", "", "weight group")
	gc := fs.Bool("gc", false, "collect unreferenced blobs afterwards")
	positional, err := e.parse(fs, args, 1, 1, "remove <repo> --group <group> [flags]")
	if err != nil {
		return err
	}
	sourceID, groupName, err := e.storedModel(ctx, *source, positional[0], *group)
	if err != nil {
		return err
	}
	resp, err := e.cl.store.RemoveModel(ctx, connect.NewRequest(&v1.RemoveModelRequest{SourceId: sourceID, Repo: positional[0], Group: groupName, Gc: *gc}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "removed %s %s\n", resp.Msg.GetModel().GetRepo(), resp.Msg.GetModel().GetGroup())
		if g := resp.Msg.GetGc(); g != nil {
			fmt.Fprintf(w, "collected %d blobs, freed %s\n", g.GetRemoved(), estimate.Human(g.GetFreedBytes()))
		}
	})
}

func runStoreStatus(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("store status"), args, 0, 0, "store status"); err != nil {
		return err
	}
	resp, err := e.cl.store.GetStatus(ctx, connect.NewRequest(&v1.GetStatusRequest{}))
	if err != nil {
		return err
	}
	st := resp.Msg.GetStatus()
	return e.print(resp.Msg, func(w io.Writer) {
		cap := "-"
		if st.GetMaxBytes() > 0 {
			cap = estimate.Human(st.GetMaxBytes())
		}
		table(w, []string{"PATH", "MODELS", "BLOBS", "BLOB BYTES", "CAP", "PARTIALS", "PARTIAL BYTES", "CACHES", "CACHE BYTES"}, [][]string{{
			st.GetPath(), strconv.FormatUint(st.GetModels(), 10), strconv.FormatUint(st.GetBlobs(), 10), estimate.Human(st.GetBlobBytes()), cap,
			strconv.FormatUint(st.GetPartials(), 10), estimate.Human(st.GetPartialBytes()),
			strconv.FormatUint(st.GetCaches(), 10), estimate.Human(st.GetCacheBytes()),
		}})
	})
}

func runStoreGc(ctx context.Context, e *env, args []string) error {
	fs := e.flags("store gc")
	partials := fs.Bool("partials", false, "also remove partial downloads")
	if _, err := e.parse(fs, args, 0, 0, "store gc [--partials]"); err != nil {
		return err
	}
	resp, err := e.cl.store.Gc(ctx, connect.NewRequest(&v1.GcRequest{Partials: *partials}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "collected %d files, freed %s\n", resp.Msg.GetRemoved(), estimate.Human(resp.Msg.GetFreedBytes()))
	})
}

func runStoreVerify(ctx context.Context, e *env, args []string) error {
	fs := e.flags("store verify")
	source := fs.String("source", "", "limit to one source")
	group := fs.String("group", "", "limit to one group")
	positional, err := e.parse(fs, args, 0, 1, "store verify [repo] [flags]")
	if err != nil {
		return err
	}
	req := &v1.VerifyRequest{SourceId: *source, Group: *group}
	if len(positional) == 1 {
		req.Repo = positional[0]
	}
	resp, err := e.cl.store.Verify(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.done(e.follow(ctx, resp.Msg.GetTask().GetId()))
}

func runStoreExport(ctx context.Context, e *env, args []string) error {
	fs := e.flags("store export")
	source := fs.String("source", "", "limit to one source")
	group := fs.String("group", "", "limit to one group")
	dir := fs.String("dir", "", "mirror directory to write, required")
	usage := "store export --dir <mirror dir> [repo] [flags]"
	positional, err := e.parse(fs, args, 0, 1, usage)
	if err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("usage: nebu %s", usage)
	}
	req := &v1.ExportRequest{SourceId: *source, Group: *group, Dir: *dir}
	if len(positional) == 1 {
		req.Repo = positional[0]
	}
	resp, err := e.cl.store.Export(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.done(e.follow(ctx, resp.Msg.GetTask().GetId()))
}

func runTasksList(ctx context.Context, e *env, args []string) error {
	fs := e.flags("tasks list")
	active := fs.Bool("active", false, "only tasks still running")
	if _, err := e.parse(fs, args, 0, 0, "tasks list [--active]"); err != nil {
		return err
	}
	resp, err := e.cl.tasks.ListTasks(ctx, connect.NewRequest(&v1.ListTasksRequest{ActiveOnly: *active}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, t := range resp.Msg.GetTasks() {
			p := t.GetProgress()
			progress := "-"
			if p.GetTotal() > 0 {
				progress = fmt.Sprintf("%s/%s", estimate.Human(p.GetDone()), estimate.Human(p.GetTotal()))
			}
			rows = append(rows, []string{t.GetId(), t.GetKind(), loud(t.GetState()), progress, t.GetTitle(), t.GetError()})
		}
		table(w, []string{"ID", "KIND", "STATE", "PROGRESS", "TITLE", "ERROR"}, rows)
	})
}

func runTasksWatch(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("tasks watch"), args, 1, 1, "tasks watch <id>")
	if err != nil {
		return err
	}
	return e.done(e.follow(ctx, positional[0]))
}

func runTasksCancel(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("tasks cancel"), args, 1, 1, "tasks cancel <id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.tasks.CancelTask(ctx, connect.NewRequest(&v1.CancelTaskRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "%s %s\n", resp.Msg.GetTask().GetId(), text.Enum(resp.Msg.GetTask().GetState()))
	})
}
