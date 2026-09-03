package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Fails commands whose state lives only in a running daemon
func (e *env) requireDaemon() error {
	if !e.remote {
		return fmt.Errorf("this command needs a running daemon, start nebu serve or pass --addr")
	}
	return nil
}

func runRuntimesAdopt(ctx context.Context, e *env, args []string) error {
	fs := e.flags("runtimes adopt")
	path := fs.String("path", "", "executable path, searched on PATH when empty")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu runtimes adopt <runtime> [--path P]")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.runtimes.AdoptInstall(ctx, connect.NewRequest(&v1.AdoptInstallRequest{RuntimeId: positional[0], Path: *path}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { installsTable(w, []*v1.Install{resp.Msg.GetInstall()}) })
}

func runRuntimesInstall(ctx context.Context, e *env, args []string) error {
	fs := e.flags("runtimes install")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu runtimes install <runtime>")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.runtimes.InstallPrebuilt(ctx, connect.NewRequest(&v1.InstallPrebuiltRequest{RuntimeId: positional[0]}))
	if err != nil {
		return err
	}
	return e.follow(ctx, cl, resp.Msg.GetTask().GetId())
}

func runRuntimesInstalls(ctx context.Context, e *env, args []string) error {
	fs := e.flags("runtimes installs")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	req := &v1.ListInstallsRequest{}
	if len(positional) == 1 {
		req.RuntimeId = positional[0]
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.runtimes.ListInstalls(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { installsTable(w, resp.Msg.GetInstalls()) })
}

func runRuntimesRemove(ctx context.Context, e *env, args []string) error {
	fs := e.flags("runtimes remove")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu runtimes remove <install-id>")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.runtimes.RemoveInstall(ctx, connect.NewRequest(&v1.RemoveInstallRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "removed %s\n", resp.Msg.GetInstall().GetId()) })
}

func installsTable(w io.Writer, list []*v1.Install) {
	var rows [][]string
	for _, in := range list {
		rows = append(rows, []string{in.GetId(), in.GetRuntimeId(), eval.EnumShort(in.GetKind()), in.GetVersion(), in.GetPath(), compact(in.GetFacts())})
	}
	table(w, []string{"ID", "RUNTIME", "KIND", "VERSION", "PATH", "FACTS"}, rows)
}

// Watches a task, printing JSON when asked
func (e *env) follow(ctx context.Context, cl *clients, id string) error {
	if e.json {
		final, err := e.watchSilently(ctx, cl, id)
		if err != nil {
			return err
		}
		return e.print(final, nil)
	}
	_, err := e.watch(ctx, cl, id)
	return err
}

func runRun(ctx context.Context, e *env, args []string) error {
	fs := e.flags("run")
	source := fs.String("source", "", "source id, first configured when empty")
	group := fs.String("group", "", "weight group, required when several are stored")
	runtimeID := fs.String("runtime", "", "runtime id, the first that accepts the format when empty")
	installID := fs.String("install", "", "install id, newest for the runtime when empty")
	name := fs.String("name", "", "public model name for the gateway")
	var params multi
	fs.Var(&params, "param", "runtime param as name=value, repeatable")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu run <repo> [flags]")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
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
	rtID, err := e.defaultRuntime(ctx, cl, sourceID, positional[0], groupName, *runtimeID)
	if err != nil {
		return err
	}
	req := &v1.RunRequest{SourceId: sourceID, Repo: positional[0], Group: groupName, RuntimeId: rtID, InstallId: *installID, Name: *name, Params: map[string]string{}}
	for _, p := range params {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return fmt.Errorf("param %q: expected name=value", p)
		}
		req.Params[k] = v
	}
	resp, err := cl.instances.Run(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	in := resp.Msg.GetInstance()
	if e.json {
		if _, err := e.watchSilently(ctx, cl, resp.Msg.GetTask().GetId()); err != nil {
			return err
		}
		final, err := cl.instances.GetInstance(ctx, connect.NewRequest(&v1.GetInstanceRequest{Id: in.GetId()}))
		if err != nil {
			return err
		}
		return e.print(final.Msg, nil)
	}
	fmt.Fprintf(e.out, "instance %s %s on %s\n", in.GetId(), in.GetName(), in.GetRuntimeId())
	if plan := in.GetPlan(); plan != nil {
		fmt.Fprintf(e.out, "plan %s device %s host %s %s\n", strings.ToUpper(eval.EnumShort(plan.GetVerdict())), poolUsage(plan, v1.PoolKind_POOL_KIND_DEVICE), poolUsage(plan, v1.PoolKind_POOL_KIND_HOST), placements(plan))
	}
	if _, err := e.watch(ctx, cl, resp.Msg.GetTask().GetId()); err != nil {
		return err
	}
	final, err := cl.instances.GetInstance(ctx, connect.NewRequest(&v1.GetInstanceRequest{Id: in.GetId()}))
	if err != nil {
		return err
	}
	if final.Msg.GetInstance().GetState() != v1.InstanceState_INSTANCE_STATE_READY {
		return fmt.Errorf("%s is %s: %s", in.GetName(), eval.EnumShort(final.Msg.GetInstance().GetState()), final.Msg.GetInstance().GetError())
	}
	fmt.Fprintf(e.out, "gateway %s/v1 model %s\n", e.gatewayBase(), in.GetName())
	return nil
}

// Picks the first compatible runtime that accepts the stored format
func (e *env) defaultRuntime(ctx context.Context, cl *clients, source, repo, group, id string) (string, error) {
	if id != "" {
		return id, nil
	}
	model, err := cl.store.GetModel(ctx, connect.NewRequest(&v1.GetModelRequest{SourceId: source, Repo: repo, Group: group}))
	if err != nil {
		return "", err
	}
	resp, err := cl.runtimes.ListRuntimes(ctx, connect.NewRequest(&v1.ListRuntimesRequest{}))
	if err != nil {
		return "", err
	}
	for _, rt := range resp.Msg.GetRuntimes() {
		for _, f := range rt.GetManifest().GetFormats() {
			if f == model.Msg.GetModel().GetFormatId() && rt.GetCompatible() {
				return rt.GetManifest().GetId(), nil
			}
		}
	}
	return "", fmt.Errorf("no compatible runtime accepts %s, pass --runtime", model.Msg.GetModel().GetFormatId())
}

func (e *env) gatewayBase() string {
	if addr := e.cfg.GetGateway().GetListen(); addr != "" {
		return "http://" + addr
	}
	addr := e.cfg.GetAddr()
	if addr == "" {
		addr = e.cfg.GetListen()
	}
	if strings.Contains(addr, "://") {
		return addr
	}
	return "http://" + addr
}

func runPs(ctx context.Context, e *env, args []string) error {
	fs := e.flags("ps")
	all := fs.Bool("all", false, "include stopped and failed instances")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.instances.ListInstances(ctx, connect.NewRequest(&v1.ListInstancesRequest{RunningOnly: !*all}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, in := range resp.Msg.GetInstances() {
			detail := in.GetError()
			if len(in.GetTriage()) > 0 {
				detail = in.GetTriage()[0].GetSummary()
			}
			rows = append(rows, []string{in.GetId(), in.GetName(), strings.ToUpper(eval.EnumShort(in.GetState())), in.GetRuntimeId(), strconv.Itoa(int(in.GetPid())), in.GetEndpoint(), measured(in), detail})
		}
		table(w, []string{"ID", "NAME", "STATE", "RUNTIME", "PID", "ENDPOINT", "DEVICE", "DETAIL"}, rows)
	})
}

func measured(in *v1.Instance) string {
	for _, m := range in.GetMeasurements() {
		if m.GetKey() == estimate.DeviceUsedKey {
			return estimate.Human(m.GetBytes())
		}
	}
	if in.GetPlan() != nil {
		return "~" + estimate.Human(estimate.PlannedDevice(in.GetPlan()))
	}
	return "-"
}

func runStop(ctx context.Context, e *env, args []string) error {
	fs := e.flags("stop")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu stop <name|id>")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.instances.StopInstance(ctx, connect.NewRequest(&v1.StopInstanceRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "%s %s\n", resp.Msg.GetInstance().GetName(), eval.EnumShort(resp.Msg.GetInstance().GetState()))
	})
}

func runLogs(ctx context.Context, e *env, args []string) error {
	fs := e.flags("logs")
	follow := fs.Bool("follow", false, "keep streaming until the instance exits")
	tail := fs.Uint("tail", 200, "lines to start from, 0 for everything retained")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu logs <name|id> [--follow] [--tail N]")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	stream, err := cl.instances.Logs(ctx, connect.NewRequest(&v1.LogsRequest{Id: positional[0], Follow: *follow, Tail: uint32(*tail)}))
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
		for _, line := range stream.Msg().GetLines() {
			fmt.Fprintln(e.out, line)
		}
	}
	return stream.Err()
}

func runShow(ctx context.Context, e *env, args []string) error {
	fs := e.flags("show")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu show <name|id>")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.instances.GetInstance(ctx, connect.NewRequest(&v1.GetInstanceRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { renderInstance(w, resp.Msg.GetInstance()) })
}

func renderInstance(w io.Writer, in *v1.Instance) {
	fmt.Fprintf(w, "%s %s %s\n", in.GetId(), in.GetName(), strings.ToUpper(eval.EnumShort(in.GetState())))
	relaunch := "no"
	if in.GetDesiredRunning() {
		relaunch = "yes"
	}
	rows := [][]string{
		{"model", in.GetSourceId() + "/" + in.GetRepo() + " " + in.GetGroup()},
		{"runtime", in.GetRuntimeId() + " install " + in.GetInstallId()},
		{"endpoint", in.GetEndpoint()},
		{"pid", strconv.Itoa(int(in.GetPid()))},
		{"device", measured(in)},
		{"created", stamp(in.GetCreatedAt())},
		{"ready", stamp(in.GetReadyAt())},
		{"stopped", stamp(in.GetStoppedAt())},
		{"relaunch on daemon start", relaunch},
		{"task", in.GetTaskId()},
	}
	if in.GetError() != "" {
		rows = append(rows, []string{"error", in.GetError()})
	}
	table(w, nil, rows)
	if plan := in.GetPlan(); plan != nil {
		section(w, "plan")
		fmt.Fprintf(w, "%s device %s host %s cache %s %s %s\n", strings.ToUpper(eval.EnumShort(plan.GetVerdict())), poolUsage(plan, v1.PoolKind_POOL_KIND_DEVICE), poolUsage(plan, v1.PoolKind_POOL_KIND_HOST), estimate.Human(plan.GetCacheBytes()), placements(plan), plan.GetDetail())
	}
	section(w, "params")
	keys := make([]string, 0, len(in.GetParams()))
	for k := range in.GetParams() {
		keys = append(keys, k)
	}
	sortStrings(keys)
	rows = nil
	for _, k := range keys {
		rows = append(rows, []string{k, in.GetParams()[k]})
	}
	table(w, nil, rows)
	if len(in.GetMeasurements()) > 0 {
		section(w, "measurements")
		rows = nil
		for _, m := range in.GetMeasurements() {
			rows = append(rows, []string{m.GetKey(), estimate.Human(m.GetBytes()), m.GetLine()})
		}
		table(w, []string{"KEY", "BYTES", "LINE"}, rows)
	}
	if len(in.GetTriage()) > 0 {
		section(w, "triage")
		for _, hit := range in.GetTriage() {
			fmt.Fprintf(w, "%s: %s\n  hint: %s\n", hit.GetId(), hit.GetSummary(), hit.GetHint())
			if len(hit.GetFix()) > 0 {
				fmt.Fprintf(w, "  try: --param %s\n", strings.ReplaceAll(compact(hit.GetFix()), " ", " --param "))
			}
			fmt.Fprintf(w, "  line: %s\n", hit.GetLine())
		}
	}
	section(w, "command")
	fmt.Fprintln(w, strings.Join(in.GetCommand(), " "))
}

func stamp(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return "-"
	}
	return ts.AsTime().Local().Format(time.RFC3339)
}
