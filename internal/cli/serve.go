package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func runRun(ctx context.Context, e *env, args []string) error {
	fs := e.flags("run")
	source := fs.String("source", "", "source id, first configured when empty")
	group := fs.String("group", "", "weight group, required when several are stored")
	runtimeID := fs.String("runtime", "", "runtime id, the first that accepts the format when empty")
	installID := fs.String("install", "", "install id, newest for the runtime when empty")
	name := fs.String("name", "", "public model name for the gateway")
	slot := fs.String("slot", "", "slot to run in, its name becomes the public name")
	force := fs.Bool("force", false, "launch even when the plan says the model does not fit, redoing any prepare step")
	var params multi
	fs.Var(&params, "param", "runtime param as name=value, repeatable, over the slot defaults")
	positional, err := e.parse(fs, args, 1, 1, "run <repo> [flags]")
	if err != nil {
		return err
	}
	paramMap, err := pairs(params, "param")
	if err != nil {
		return err
	}
	sourceID, groupName, err := e.storedModel(ctx, *source, positional[0], *group)
	if err != nil {
		return err
	}
	req := &v1.RunRequest{SourceId: sourceID, Repo: positional[0], Group: groupName, RuntimeId: *runtimeID, InstallId: *installID, Name: *name, Params: paramMap, SlotId: *slot, Force: *force}
	resp, err := e.cl.instances.Run(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	in := resp.Msg.GetInstance()
	e.text("instance %s %s on %s\n", in.GetId(), in.GetName(), in.GetRuntimeId())
	if plan := in.GetPlan(); plan != nil {
		e.text("plan %s\n", planLine(plan))
	}
	if _, err := e.follow(ctx, resp.Msg.GetTask().GetId()); err != nil {
		return err
	}
	final, err := e.cl.instances.GetInstance(ctx, connect.NewRequest(&v1.GetInstanceRequest{Id: in.GetId()}))
	if err != nil {
		return err
	}
	if st := final.Msg.GetInstance(); st.GetState() != v1.InstanceState_INSTANCE_STATE_READY {
		return fmt.Errorf("%s is %s: %s", in.GetName(), eval.EnumShort(st.GetState()), st.GetError())
	}
	return e.print(final.Msg, func(w io.Writer) { fmt.Fprintf(w, "gateway %s/v1 model %s\n", e.gatewayBase(ctx), in.GetName()) })
}

func runPs(ctx context.Context, e *env, args []string) error {
	fs := e.flags("ps")
	all := fs.Bool("all", false, "include stopped and failed instances")
	if _, err := e.parse(fs, args, 0, 0, "ps [--all]"); err != nil {
		return err
	}
	resp, err := e.cl.instances.ListInstances(ctx, connect.NewRequest(&v1.ListInstancesRequest{RunningOnly: !*all}))
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
			rows = append(rows, []string{in.GetId(), in.GetName(), loud(in.GetState()), in.GetRuntimeId(), strconv.Itoa(int(in.GetPid())), in.GetEndpoint(), measured(in), detail})
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
	positional, err := e.parse(e.flags("stop"), args, 1, 1, "stop <name|id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.instances.StopInstance(ctx, connect.NewRequest(&v1.StopInstanceRequest{Id: positional[0]}))
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
	positional, err := e.parse(fs, args, 1, 1, "logs <name|id> [--follow] [--tail N]")
	if err != nil {
		return err
	}
	stream, err := e.cl.instances.Logs(ctx, connect.NewRequest(&v1.LogsRequest{Id: positional[0], Follow: *follow, Tail: uint32(*tail)}))
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
	positional, err := e.parse(e.flags("show"), args, 1, 1, "show <name|id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.instances.GetInstance(ctx, connect.NewRequest(&v1.GetInstanceRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { renderInstance(w, resp.Msg.GetInstance()) })
}

func renderInstance(w io.Writer, in *v1.Instance) {
	fmt.Fprintf(w, "%s %s %s\n", in.GetId(), in.GetName(), loud(in.GetState()))
	rows := [][]string{
		{"model", in.GetSourceId() + "/" + in.GetRepo() + " " + in.GetGroup()},
		{"runtime", in.GetRuntimeId() + " install " + in.GetInstallId()},
		{"endpoint", in.GetEndpoint()},
		{"pid", strconv.Itoa(int(in.GetPid()))},
		{"device", measured(in)},
		{"created", when(in.GetCreatedAt(), time.RFC3339)},
		{"ready", when(in.GetReadyAt(), time.RFC3339)},
		{"stopped", when(in.GetStoppedAt(), time.RFC3339)},
		{"relaunch on daemon start", yes(in.GetDesiredRunning())},
		{"task", in.GetTaskId()},
	}
	if in.GetError() != "" {
		rows = append(rows, []string{"error", in.GetError()})
	}
	table(w, nil, rows)
	if plan := in.GetPlan(); plan != nil {
		section(w, "plan")
		fmt.Fprintln(w, planLine(plan))
	}
	section(w, "params")
	table(w, nil, rowsOf(in.GetParams()))
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

func runSwap(ctx context.Context, e *env, args []string) error {
	fs := e.flags("swap")
	source := fs.String("source", "", "source id, first configured when empty")
	group := fs.String("group", "", "weight group, required when several are stored")
	runtimeID := fs.String("runtime", "", "runtime id, the slot default or the first that accepts the format when empty")
	installID := fs.String("install", "", "install id, newest for the runtime when empty")
	drainFirst := fs.Bool("drain-first", false, "stop the old instance before starting the new one even when both fit")
	force := fs.Bool("force", false, "launch even when the plan says the model does not fit, redoing any prepare step")
	var params multi
	fs.Var(&params, "param", "runtime param as name=value, repeatable, over the slot defaults")
	positional, err := e.parse(fs, args, 2, 2, "swap <slot> <repo> [flags]")
	if err != nil {
		return err
	}
	paramMap, err := pairs(params, "param")
	if err != nil {
		return err
	}
	sourceID, groupName, err := e.storedModel(ctx, *source, positional[1], *group)
	if err != nil {
		return err
	}
	run := &v1.RunRequest{SourceId: sourceID, Repo: positional[1], Group: groupName, RuntimeId: *runtimeID, InstallId: *installID, Params: paramMap, Force: *force}
	resp, err := e.cl.slots.Swap(ctx, connect.NewRequest(&v1.SwapRequest{SlotId: positional[0], Run: run, DrainFirst: *drainFirst}))
	if err != nil {
		return err
	}
	e.text("slot %s %s\n", resp.Msg.GetSlot().GetName(), eval.EnumShort(resp.Msg.GetSlot().GetState()))
	if _, err := e.follow(ctx, resp.Msg.GetTask().GetId()); err != nil {
		return err
	}
	final, err := e.cl.slots.GetSlot(ctx, connect.NewRequest(&v1.GetSlotRequest{Id: resp.Msg.GetSlot().GetId()}))
	if err != nil {
		return err
	}
	s := final.Msg.GetSlot()
	if s.GetState() != v1.SlotState_SLOT_STATE_READY {
		return fmt.Errorf("slot %s is %s: %s", s.GetName(), eval.EnumShort(s.GetState()), s.GetError())
	}
	return e.print(final.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "gateway %s/v1 model %s serves %s\n", e.gatewayBase(ctx), s.GetName(), modelText(s.GetRequest()))
	})
}

func runSlotsList(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("slots list"), args, 0, 0, "slots list"); err != nil {
		return err
	}
	resp, err := e.cl.slots.ListSlots(ctx, connect.NewRequest(&v1.ListSlotsRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { slotsTable(w, resp.Msg.GetSlots()) })
}

func slotsTable(w io.Writer, list []*v1.Slot) {
	var rows [][]string
	for _, s := range list {
		budget, devices := "-", "all"
		if s.GetMemoryBytes() > 0 {
			budget = estimate.Human(s.GetMemoryBytes())
		}
		if len(s.GetDeviceIds()) > 0 {
			devices = strings.Join(s.GetDeviceIds(), ",")
		}
		rows = append(rows, []string{s.GetId(), s.GetName(), loud(s.GetState()), modelText(s.GetRequest()), s.GetInstanceId(), devices, budget, policyText(s.GetPolicy()), s.GetError()})
	}
	table(w, []string{"ID", "NAME", "STATE", "MODEL", "INSTANCE", "DEVICES", "BUDGET", "LIMITS", "ERROR"}, rows)
}

func modelText(req *v1.RunRequest) string {
	if req == nil {
		return "-"
	}
	return req.GetRepo() + ":" + req.GetGroup()
}

// Declares the slot settings flags, the returned func reads them once parsed
func slotFlags(fs *flag.FlagSet) (*v1.UpdateSlotRequest, func() error) {
	req := &v1.UpdateSlotRequest{}
	var devices, params multi
	var position uint
	fs.Var(&devices, "device", "device id from nebu host, repeatable, all devices when none")
	memory := fs.String("memory", "", "device memory budget such as 8GiB, whole devices when empty")
	fs.StringVar(&req.Description, "description", "", "free text")
	fs.StringVar(&req.RuntimeId, "runtime", "", "default runtime for models run in the slot")
	fs.UintVar(&position, "position", 0, "place in the slot list, one based, last when 0")
	fs.Var(&params, "param", "default runtime param as name=value, repeatable")
	policy, limits := policyFlags(fs)
	req.Policy = policy
	return req, func() error {
		req.Position = uint32(position)
		var err error
		if req.MemoryBytes, err = parseMemory(*memory); err != nil {
			return err
		}
		if req.Params, err = pairs(params, "param"); err != nil {
			return err
		}
		for _, d := range devices {
			if d != "" {
				req.DeviceIds = append(req.DeviceIds, d)
			}
		}
		return limits()
	}
}

func runSlotsCreate(ctx context.Context, e *env, args []string) error {
	fs := e.flags("slots create")
	settings, read := slotFlags(fs)
	positional, err := e.parse(fs, args, 1, 1, "slots create <name> [--position N] [--device ID] [--memory 8GiB] [--runtime R] [--param k=v] [--max-in-flight N] [--rps R] [--burst N] [--timeout D] [--upstream-timeout D]")
	if err != nil {
		return err
	}
	if err := read(); err != nil {
		return err
	}
	resp, err := e.cl.slots.CreateSlot(ctx, connect.NewRequest(&v1.CreateSlotRequest{Name: positional[0], Description: settings.Description, DeviceIds: settings.DeviceIds, MemoryBytes: settings.MemoryBytes, RuntimeId: settings.RuntimeId, Params: settings.Params, Policy: settings.Policy, Position: settings.Position}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { slotsTable(w, []*v1.Slot{resp.Msg.GetSlot()}) })
}

func runSlotsUpdate(ctx context.Context, e *env, args []string) error {
	fs := e.flags("slots update")
	settings, read := slotFlags(fs)
	rename := fs.String("name", "", "a new public name, the route following it")
	positional, err := e.parse(fs, args, 1, 1, "slots update <name|id> [--name NEW] [--position N] [flags]")
	if err != nil {
		return err
	}
	if err := read(); err != nil {
		return err
	}
	current, err := e.cl.slots.GetSlot(ctx, connect.NewRequest(&v1.GetSlotRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	// The update replaces every field, so start from what the slot has and change only what was passed
	cur := current.Msg.GetSlot()
	req := &v1.UpdateSlotRequest{Id: cur.GetId(), Description: cur.GetDescription(), DeviceIds: cur.GetDeviceIds(), MemoryBytes: cur.GetMemoryBytes(), RuntimeId: cur.GetRuntimeId(), Params: cur.GetParams(), Policy: cur.GetPolicy()}
	if req.Policy == nil {
		req.Policy = &v1.Policy{}
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "max-in-flight":
			req.Policy.MaxInFlight = settings.Policy.MaxInFlight
		case "rps":
			req.Policy.RequestsPerSecond = settings.Policy.RequestsPerSecond
		case "burst":
			req.Policy.Burst = settings.Policy.Burst
		case "timeout":
			req.Policy.RequestTimeoutMs = settings.Policy.RequestTimeoutMs
		case "upstream-timeout":
			req.Policy.UpstreamTimeoutMs = settings.Policy.UpstreamTimeoutMs
		case "device":
			req.DeviceIds = settings.DeviceIds
		case "memory":
			req.MemoryBytes = settings.MemoryBytes
		case "description":
			req.Description = settings.Description
		case "runtime":
			req.RuntimeId = settings.RuntimeId
		case "param":
			req.Params = settings.Params
		case "position":
			req.Position = settings.Position
		case "name":
			req.Name = *rename
		}
	})
	resp, err := e.cl.slots.UpdateSlot(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { slotsTable(w, []*v1.Slot{resp.Msg.GetSlot()}) })
}

// Declares the route limit flags, the returned func fills the policy once parsed
func policyFlags(fs *flag.FlagSet) (*v1.Policy, func() error) {
	p := &v1.Policy{}
	inFlight := fs.Uint("max-in-flight", 0, "requests in flight at once on the slot's route, 0 inherits the gateway default")
	rps := fs.Float64("rps", 0, "sustained requests per second, 0 inherits")
	burst := fs.Uint("burst", 0, "requests a quiet route absorbs at once, the rate rounded up when 0")
	timeout := fs.String("timeout", "", "whole request timeout such as 60s or 5m, 0 inherits")
	upstream := fs.String("upstream-timeout", "", "how long the runtime may take to start answering, such as 10m")
	return p, func() error {
		p.MaxInFlight, p.RequestsPerSecond, p.Burst = uint32(*inFlight), *rps, uint32(*burst)
		var err error
		if p.RequestTimeoutMs, err = parseMillis(*timeout); err != nil {
			return fmt.Errorf("timeout: %w", err)
		}
		if p.UpstreamTimeoutMs, err = parseMillis(*upstream); err != nil {
			return fmt.Errorf("upstream-timeout: %w", err)
		}
		return nil
	}
}

func parseMillis(s string) (uint32, error) {
	if strings.TrimSpace(s) == "" || s == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return uint32(d / time.Millisecond), nil
}

// Puts a policy into one line, dash when it inherits everything
func policyText(p *v1.Policy) string {
	var parts []string
	if p.GetMaxInFlight() > 0 {
		parts = append(parts, fmt.Sprintf("in-flight %d", p.GetMaxInFlight()))
	}
	if p.GetRequestsPerSecond() > 0 {
		s := fmt.Sprintf("%g/s", p.GetRequestsPerSecond())
		if p.GetBurst() > 0 {
			s += fmt.Sprintf(" burst %d", p.GetBurst())
		}
		parts = append(parts, s)
	}
	if p.GetRequestTimeoutMs() > 0 {
		parts = append(parts, "timeout "+(time.Duration(p.GetRequestTimeoutMs())*time.Millisecond).String())
	}
	if p.GetUpstreamTimeoutMs() > 0 {
		parts = append(parts, "upstream "+(time.Duration(p.GetUpstreamTimeoutMs())*time.Millisecond).String())
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func parseMemory(s string) (uint64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	n, err := eval.Bytes(s, "")
	if err != nil {
		return 0, fmt.Errorf("memory %q: %w", s, err)
	}
	return n, nil
}

func runSlotsShow(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("slots show"), args, 1, 1, "slots show <name|id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.slots.GetSlot(ctx, connect.NewRequest(&v1.GetSlotRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		s := resp.Msg.GetSlot()
		fmt.Fprintf(w, "%s %s %s\n", s.GetId(), s.GetName(), loud(s.GetState()))
		rows := [][]string{
			{"description", s.GetDescription()},
			{"devices", strings.Join(s.GetDeviceIds(), ", ")},
			{"budget", estimate.Human(s.GetMemoryBytes())},
			{"runtime", s.GetRuntimeId()},
			{"params", compact(s.GetParams())},
			{"limits", policyText(s.GetPolicy())},
			{"model", modelText(s.GetRequest())},
			{"instance", s.GetInstanceId()},
			{"task", s.GetTaskId()},
			{"created", when(s.GetCreatedAt(), time.RFC3339)},
			{"updated", when(s.GetUpdatedAt(), time.RFC3339)},
		}
		if s.GetError() != "" {
			rows = append(rows, []string{"error", s.GetError()})
		}
		table(w, nil, rows)
		if in := resp.Msg.GetInstance(); in != nil {
			section(w, "occupant")
			renderInstance(w, in)
		}
	})
}

func runSlotsEvict(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("slots evict"), args, 1, 1, "slots evict <name|id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.slots.EvictSlot(ctx, connect.NewRequest(&v1.EvictSlotRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { slotsTable(w, []*v1.Slot{resp.Msg.GetSlot()}) })
}

func runSlotsRelaunch(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("slots relaunch"), args, 1, 1, "slots relaunch <name|id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.slots.RelaunchSlot(ctx, connect.NewRequest(&v1.RelaunchSlotRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	e.text("slot %s %s\n", resp.Msg.GetSlot().GetName(), eval.EnumShort(resp.Msg.GetSlot().GetState()))
	if _, err := e.follow(ctx, resp.Msg.GetTask().GetId()); err != nil {
		return err
	}
	final, err := e.cl.slots.GetSlot(ctx, connect.NewRequest(&v1.GetSlotRequest{Id: resp.Msg.GetSlot().GetId()}))
	if err != nil {
		return err
	}
	return e.print(final.Msg, func(w io.Writer) { slotsTable(w, []*v1.Slot{final.Msg.GetSlot()}) })
}

func runSlotsRemove(ctx context.Context, e *env, args []string) error {
	fs := e.flags("slots remove")
	force := fs.Bool("force", false, "stop the occupant first")
	positional, err := e.parse(fs, args, 1, 1, "slots remove <name|id> [--force]")
	if err != nil {
		return err
	}
	resp, err := e.cl.slots.DeleteSlot(ctx, connect.NewRequest(&v1.DeleteSlotRequest{Id: positional[0], Force: *force}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "removed %s\n", resp.Msg.GetSlot().GetName()) })
}

func runRoutesList(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("routes list"), args, 0, 0, "routes list"); err != nil {
		return err
	}
	resp, err := e.cl.gateway.ListRoutes(ctx, connect.NewRequest(&v1.ListRoutesRequest{}))
	if err != nil {
		return err
	}
	defaults := e.gatewayStatus(ctx).GetPolicy()
	return e.print(resp.Msg, func(w io.Writer) { routesTable(w, resp.Msg.GetRoutes(), defaults) })
}

// Prints routes with the limits each one enforces, its own over the gateway's
func routesTable(w io.Writer, list []*v1.Route, defaults *v1.Policy) {
	var rows [][]string
	for _, r := range list {
		rows = append(rows, []string{r.GetName(), loud(r.GetState()), r.GetModel(), r.GetInstanceId(), r.GetSlotId(), r.GetEndpoint(), strconv.FormatUint(r.GetRequests(), 10), strconv.Itoa(int(r.GetInFlight())), policyText(gateway.Effective(r.GetPolicy(), defaults))})
	}
	table(w, []string{"NAME", "STATE", "MODEL", "INSTANCE", "SLOT", "ENDPOINT", "REQUESTS", "IN FLIGHT", "LIMITS"}, rows)
}

func runRoutesAdd(ctx context.Context, e *env, args []string) error {
	fs := e.flags("routes add")
	policy, limits := policyFlags(fs)
	positional, err := e.parse(fs, args, 2, 2, "routes add <name> <instance> [--max-in-flight N] [--rps R] [--burst N] [--timeout D] [--upstream-timeout D]")
	if err != nil {
		return err
	}
	if err := limits(); err != nil {
		return err
	}
	in, err := e.cl.instances.GetInstance(ctx, connect.NewRequest(&v1.GetInstanceRequest{Id: positional[1]}))
	if err != nil {
		return err
	}
	resp, err := e.cl.gateway.SetRoute(ctx, connect.NewRequest(&v1.SetRouteRequest{Name: positional[0], InstanceId: in.Msg.GetInstance().GetId(), Policy: policy}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		routesTable(w, []*v1.Route{resp.Msg.GetRoute()}, e.gatewayStatus(ctx).GetPolicy())
	})
}

func runRoutesRemove(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("routes remove"), args, 1, 1, "routes remove <name>")
	if err != nil {
		return err
	}
	resp, err := e.cl.gateway.DeleteRoute(ctx, connect.NewRequest(&v1.DeleteRouteRequest{Name: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "removed %s\n", resp.Msg.GetRoute().GetName()) })
}

func runGateway(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("gateway"), args, 0, 0, "gateway"); err != nil {
		return err
	}
	resp, err := e.cl.gateway.GetGatewayStatus(ctx, connect.NewRequest(&v1.GetGatewayStatusRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		st := resp.Msg.GetStatus()
		scheme := "http"
		if st.GetTls() {
			scheme = "https"
		}
		for _, l := range st.GetListeners() {
			shared := ""
			if l.GetShared() {
				shared = " shared with the api"
			}
			fmt.Fprintf(w, "listening %s://%s/v1 and /api%s\n", scheme, l.GetAddr(), shared)
		}
		fmt.Fprintf(w, "auth %t tls %t requests %d default limits %s\n", st.GetAuth(), st.GetTls(), st.GetRequests(), policyText(st.GetPolicy()))
		section(w, "routes")
		routesTable(w, st.GetRoutes(), st.GetPolicy())
	})
}

func runGatewayTraces(ctx context.Context, e *env, args []string) error {
	fs := e.flags("gateway traces")
	route := fs.String("route", "", "one public name, every route when empty")
	limit := fs.Uint("limit", 50, "newest traces to show, 0 for everything kept")
	if _, err := e.parse(fs, args, 0, 0, "gateway traces [--route NAME] [--limit N]"); err != nil {
		return err
	}
	resp, err := e.cl.gateway.ListTraces(ctx, connect.NewRequest(&v1.ListTracesRequest{Route: *route, Limit: uint32(*limit)}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, t := range resp.Msg.GetTraces() {
			rows = append(rows, []string{t.GetId(), when(t.GetStartedAt(), time.RFC3339), t.GetRoute(), eval.EnumShort(t.GetKind()), traceFormat(t), strconv.Itoa(int(t.GetStatus())), traceMillis(t.GetStartedAt(), t.GetFirstTokenAt()), traceMillis(t.GetStartedAt(), t.GetFinishedAt()), strconv.Itoa(int(t.GetPromptTokens())), strconv.Itoa(int(t.GetCompletionTokens())), t.GetStop(), t.GetError()})
		}
		table(w, []string{"ID", "STARTED", "ROUTE", "KIND", "FORMAT", "STATUS", "FIRST TOKEN", "TOTAL", "IN", "OUT", "STOP", "ERROR"}, rows)
	})
}

// The wire formats of a trace, one word when the runtime spoke the client's
func traceFormat(t *v1.Trace) string {
	client := eval.EnumShort(t.GetClientApi())
	if !t.GetTranslated() {
		return client
	}
	return client + ">" + eval.EnumShort(t.GetUpstreamApi())
}

// Milliseconds between two stamps, dash when either is missing
func traceMillis(from, to *timestamppb.Timestamp) string {
	if from == nil || to == nil {
		return "-"
	}
	return strconv.FormatInt(to.AsTime().Sub(from.AsTime()).Milliseconds(), 10) + "ms"
}

func runGatewayTrace(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("gateway trace"), args, 1, 1, "gateway trace <id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.gateway.GetTrace(ctx, connect.NewRequest(&v1.GetTraceRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		t := resp.Msg.GetTrace()
		rows := [][]string{
			{"id", t.GetId()},
			{"route", t.GetRoute()},
			{"instance", t.GetInstanceId()},
			{"slot", t.GetSlotId()},
			{"kind", eval.EnumShort(t.GetKind())},
			{"format", traceFormat(t)},
			{"path", t.GetPath()},
			{"stream", yes(t.GetStream())},
			{"remote", t.GetRemote()},
			{"started", when(t.GetStartedAt(), time.RFC3339Nano)},
			{"first byte", traceMillis(t.GetStartedAt(), t.GetFirstByteAt())},
			{"first token", traceMillis(t.GetStartedAt(), t.GetFirstTokenAt())},
			{"total", traceMillis(t.GetStartedAt(), t.GetFinishedAt())},
			{"status", strconv.Itoa(int(t.GetStatus()))},
			{"tokens", fmt.Sprintf("%d in, %d out", t.GetPromptTokens(), t.GetCompletionTokens())},
			{"stop", t.GetStop()},
			{"bytes", fmt.Sprintf("%d in, %d out", t.GetRequestBytes(), t.GetResponseBytes())},
		}
		if t.GetError() != "" {
			rows = append(rows, []string{"error", t.GetError()})
		}
		table(w, nil, rows)
		section(w, "request")
		fmt.Fprintln(w, t.GetRequest())
		if t.GetUpstreamRequest() != "" {
			section(w, "upstream request")
			fmt.Fprintln(w, t.GetUpstreamRequest())
		}
		for _, c := range t.GetToolCalls() {
			section(w, "tool call "+c.GetName())
			fmt.Fprintln(w, c.GetArguments())
		}
		section(w, "response")
		fmt.Fprintln(w, t.GetResponse())
	})
}

func runEvents(ctx context.Context, e *env, args []string) error {
	fs := e.flags("events")
	snapshot := fs.Bool("snapshot", false, "send the current state first")
	var kinds multi
	fs.Var(&kinds, "kind", "event kind such as instance, task, slot, route, repeatable")
	if _, err := e.parse(fs, args, 0, 0, "events [--snapshot] [--kind K]..."); err != nil {
		return err
	}
	req := &v1.WatchEventsRequest{Snapshot: *snapshot}
	for _, k := range kinds {
		n, ok := v1.EventKind_value["EVENT_KIND_"+strings.ToUpper(k)]
		if !ok {
			return fmt.Errorf("unknown event kind %q", k)
		}
		req.Kinds = append(req.Kinds, v1.EventKind(n))
	}
	stream, err := e.cl.events.WatchEvents(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	defer stream.Close()
	marshal := protojson.MarshalOptions{UseProtoNames: true}
	for stream.Receive() {
		data, err := marshal.Marshal(stream.Msg().GetEvent())
		if err != nil {
			return err
		}
		fmt.Fprintln(e.out, string(data))
	}
	if err := stream.Err(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
