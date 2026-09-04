package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func runPull(ctx context.Context, e *env, args []string) error {
	fs := e.flags("pull")
	source := fs.String("source", "", "source id, first configured when empty")
	group := fs.String("group", "", "weight group, required when the repo has several")
	detach := fs.Bool("detach", false, "start the task and return its id")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu pull <repo>[@revision] [flags]")
	}
	repo, revision := splitRef(positional[0])
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if *detach {
		if err := e.requireDaemon(); err != nil {
			return err
		}
	}
	resp, err := cl.store.Pull(ctx, connect.NewRequest(&v1.PullRequest{SourceId: *source, Repo: repo, Revision: revision, Group: *group}))
	if err != nil {
		return err
	}
	task := resp.Msg.GetTask()
	if *detach {
		return e.print(task, func(w io.Writer) { fmt.Fprintln(w, task.GetId()) })
	}
	if e.json {
		final, err := e.watchSilently(ctx, cl, task.GetId())
		if err != nil {
			return err
		}
		return e.print(final, nil)
	}
	_, err = e.watch(ctx, cl, task.GetId())
	return err
}

func (e *env) watchSilently(ctx context.Context, cl *clients, id string) (*v1.Task, error) {
	stream, err := cl.tasks.WatchTask(ctx, connect.NewRequest(&v1.WatchTaskRequest{Id: id}))
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	var last *v1.Task
	for stream.Receive() {
		last = stream.Msg().GetTask()
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if last.GetState() != v1.TaskState_TASK_STATE_SUCCEEDED {
		return last, fmt.Errorf("%s: %s", eval.EnumShort(last.GetState()), last.GetError())
	}
	return last, nil
}

func runList(ctx context.Context, e *env, args []string) error {
	fs := e.flags("list")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.store.ListModels(ctx, connect.NewRequest(&v1.ListModelsRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		stamp := func(ts *timestamppb.Timestamp) string {
			if ts == nil {
				return "-"
			}
			return ts.AsTime().Local().Format("2006-01-02 15:04")
		}
		var rows [][]string
		for _, m := range resp.Msg.GetModels() {
			// Eviction takes the model used longest ago, a model never run counting from its pull
			rows = append(rows, []string{m.GetSourceId(), m.GetRepo(), m.GetGroup(), m.GetFormatId(), m.GetDescriptor_().GetArchitecture(), estimate.Human(m.GetBytes()), stamp(m.GetPulledAt()), stamp(cmp.Or(m.GetUsedAt(), m.GetPulledAt())), m.GetPath()})
		}
		table(w, []string{"SOURCE", "REPO", "GROUP", "FORMAT", "ARCH", "SIZE", "PULLED", "USED", "PATH"}, rows)
	})
}

func runRemove(ctx context.Context, e *env, args []string) error {
	fs := e.flags("remove")
	source := fs.String("source", "", "source id, first configured when empty")
	group := fs.String("group", "", "weight group")
	gc := fs.Bool("gc", false, "collect unreferenced blobs afterwards")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu remove <repo> --group <group> [flags]")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	sourceID, err := e.defaultSource(ctx, cl, *source)
	if err != nil {
		return err
	}
	groupName, err := e.onlyGroup(ctx, cl, sourceID, positional[0], *group)
	if err != nil {
		return err
	}
	resp, err := cl.store.RemoveModel(ctx, connect.NewRequest(&v1.RemoveModelRequest{SourceId: sourceID, Repo: positional[0], Group: groupName, Gc: *gc}))
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

// Resolves an empty source id to the first configured source
func (e *env) defaultSource(ctx context.Context, cl *clients, id string) (string, error) {
	if id != "" {
		return id, nil
	}
	resp, err := cl.sources.ListSources(ctx, connect.NewRequest(&v1.ListSourcesRequest{}))
	if err != nil {
		return "", err
	}
	if len(resp.Msg.GetSources()) == 0 {
		return "", fmt.Errorf("no sources configured")
	}
	return resp.Msg.GetSources()[0].GetSource().GetId(), nil
}

// Resolves an empty group to the only stored group
func (e *env) onlyGroup(ctx context.Context, cl *clients, source, repo, group string) (string, error) {
	if group != "" {
		return group, nil
	}
	resp, err := cl.store.ListModels(ctx, connect.NewRequest(&v1.ListModelsRequest{}))
	if err != nil {
		return "", err
	}
	var names []string
	for _, m := range resp.Msg.GetModels() {
		if m.GetSourceId() == source && m.GetRepo() == repo {
			names = append(names, m.GetGroup())
		}
	}
	switch len(names) {
	case 0:
		return "", fmt.Errorf("%s is not stored", repo)
	case 1:
		return names[0], nil
	}
	return "", fmt.Errorf("%s has groups %s, pass --group", repo, strings.Join(names, ", "))
}

func runStoreStatus(ctx context.Context, e *env, args []string) error {
	fs := e.flags("store status")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.store.GetStatus(ctx, connect.NewRequest(&v1.GetStatusRequest{}))
	if err != nil {
		return err
	}
	st := resp.Msg.GetStatus()
	return e.print(resp.Msg, func(w io.Writer) {
		cap := "-"
		if st.GetMaxBytes() > 0 {
			cap = estimate.Human(st.GetMaxBytes())
		}
		table(w, []string{"PATH", "MODELS", "BLOBS", "BLOB BYTES", "CAP", "PARTIALS", "PARTIAL BYTES"}, [][]string{{
			st.GetPath(), strconv.FormatUint(st.GetModels(), 10), strconv.FormatUint(st.GetBlobs(), 10), estimate.Human(st.GetBlobBytes()), cap,
			strconv.FormatUint(st.GetPartials(), 10), estimate.Human(st.GetPartialBytes()),
		}})
	})
}

func runStoreGc(ctx context.Context, e *env, args []string) error {
	fs := e.flags("store gc")
	partials := fs.Bool("partials", false, "also remove partial downloads")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.store.Gc(ctx, connect.NewRequest(&v1.GcRequest{Partials: *partials}))
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
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	req := &v1.VerifyRequest{SourceId: *source, Group: *group}
	if len(positional) == 1 {
		req.Repo = positional[0]
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.store.Verify(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	if e.json {
		final, err := e.watchSilently(ctx, cl, resp.Msg.GetTask().GetId())
		if err != nil {
			return err
		}
		return e.print(final, nil)
	}
	_, err = e.watch(ctx, cl, resp.Msg.GetTask().GetId())
	return err
}

func runTasksList(ctx context.Context, e *env, args []string) error {
	fs := e.flags("tasks list")
	active := fs.Bool("active", false, "only tasks still running")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.tasks.ListTasks(ctx, connect.NewRequest(&v1.ListTasksRequest{ActiveOnly: *active}))
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
			rows = append(rows, []string{t.GetId(), t.GetKind(), strings.ToUpper(eval.EnumShort(t.GetState())), progress, t.GetTitle(), t.GetError()})
		}
		table(w, []string{"ID", "KIND", "STATE", "PROGRESS", "TITLE", "ERROR"}, rows)
	})
}

func runTasksWatch(ctx context.Context, e *env, args []string) error {
	fs := e.flags("tasks watch")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu tasks watch <id>")
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if e.json {
		final, err := e.watchSilently(ctx, cl, positional[0])
		if err != nil {
			return err
		}
		return e.print(final, nil)
	}
	_, err = e.watch(ctx, cl, positional[0])
	return err
}

func runTasksCancel(ctx context.Context, e *env, args []string) error {
	fs := e.flags("tasks cancel")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu tasks cancel <id>")
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.tasks.CancelTask(ctx, connect.NewRequest(&v1.CancelTaskRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "%s %s\n", resp.Msg.GetTask().GetId(), eval.EnumShort(resp.Msg.GetTask().GetState()))
	})
}
