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
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func runSlotsList(ctx context.Context, e *env, args []string) error {
	fs := e.flags("slots list")
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
	resp, err := cl.slots.ListSlots(ctx, connect.NewRequest(&v1.ListSlotsRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { slotsTable(w, resp.Msg.GetSlots()) })
}

func slotsTable(w io.Writer, list []*v1.Slot) {
	var rows [][]string
	for _, s := range list {
		budget := "-"
		if s.GetMemoryBytes() > 0 {
			budget = estimate.Human(s.GetMemoryBytes())
		}
		devices := "all"
		if len(s.GetDeviceIds()) > 0 {
			devices = strings.Join(s.GetDeviceIds(), ",")
		}
		rows = append(rows, []string{s.GetId(), s.GetName(), strings.ToUpper(eval.EnumShort(s.GetState())), modelText(s.GetRequest()), s.GetInstanceId(), devices, budget, policyText(s.GetPolicy()), s.GetError()})
	}
	table(w, []string{"ID", "NAME", "STATE", "MODEL", "INSTANCE", "DEVICES", "BUDGET", "LIMITS", "ERROR"}, rows)
}

func modelText(req *v1.RunRequest) string {
	if req == nil {
		return "-"
	}
	return req.GetRepo() + ":" + req.GetGroup()
}

func runSlotsCreate(ctx context.Context, e *env, args []string) error {
	fs := e.flags("slots create")
	var devices, params multi
	fs.Var(&devices, "device", "device id from nebu host, repeatable, all devices when none")
	memory := fs.String("memory", "", "device memory budget such as 8GiB, whole devices when empty")
	description := fs.String("description", "", "free text")
	runtimeID := fs.String("runtime", "", "default runtime for models run in the slot")
	fs.Var(&params, "param", "default runtime param as name=value, repeatable")
	policy, limits := policyFlags(fs)
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu slots create <name> [--device ID] [--memory 8GiB] [--runtime R] [--param k=v] [--max-in-flight N] [--rps R] [--burst N] [--timeout D] [--upstream-timeout D]")
	}
	bytes, err := parseMemory(*memory)
	if err != nil {
		return err
	}
	paramMap, err := parseParams(params)
	if err != nil {
		return err
	}
	if err := limits(); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.slots.CreateSlot(ctx, connect.NewRequest(&v1.CreateSlotRequest{Name: positional[0], Description: *description, DeviceIds: devices, MemoryBytes: bytes, RuntimeId: *runtimeID, Params: paramMap, Policy: policy}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { slotsTable(w, []*v1.Slot{resp.Msg.GetSlot()}) })
}

func runSlotsUpdate(ctx context.Context, e *env, args []string) error {
	fs := e.flags("slots update")
	var devices, params multi
	fs.Var(&devices, "device", "device id from nebu host, repeatable, all devices when none")
	memory := fs.String("memory", "", "device memory budget such as 8GiB, whole devices when empty")
	description := fs.String("description", "", "free text")
	runtimeID := fs.String("runtime", "", "default runtime for models run in the slot")
	fs.Var(&params, "param", "default runtime param as name=value, repeatable")
	policy, limits := policyFlags(fs)
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu slots update <name|id> [flags]")
	}
	bytes, err := parseMemory(*memory)
	if err != nil {
		return err
	}
	paramMap, err := parseParams(params)
	if err != nil {
		return err
	}
	if err := limits(); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	current, err := cl.slots.GetSlot(ctx, connect.NewRequest(&v1.GetSlotRequest{Id: positional[0]}))
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
			req.Policy.MaxInFlight = policy.MaxInFlight
		case "rps":
			req.Policy.RequestsPerSecond = policy.RequestsPerSecond
		case "burst":
			req.Policy.Burst = policy.Burst
		case "timeout":
			req.Policy.RequestTimeoutMs = policy.RequestTimeoutMs
		case "upstream-timeout":
			req.Policy.UpstreamTimeoutMs = policy.UpstreamTimeoutMs
		case "device":
			req.DeviceIds = nil
			for _, d := range devices {
				if d != "" {
					req.DeviceIds = append(req.DeviceIds, d)
				}
			}
		case "memory":
			req.MemoryBytes = bytes
		case "description":
			req.Description = *description
		case "runtime":
			req.RuntimeId = *runtimeID
		case "param":
			req.Params = paramMap
		}
	})
	resp, err := cl.slots.UpdateSlot(ctx, connect.NewRequest(req))
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

func parseParams(params []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range params {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("param %q: expected name=value", p)
		}
		out[k] = v
	}
	return out, nil
}

func runSlotsShow(ctx context.Context, e *env, args []string) error {
	fs := e.flags("slots show")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu slots show <name|id>")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.slots.GetSlot(ctx, connect.NewRequest(&v1.GetSlotRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		s := resp.Msg.GetSlot()
		fmt.Fprintf(w, "%s %s %s\n", s.GetId(), s.GetName(), strings.ToUpper(eval.EnumShort(s.GetState())))
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
			{"created", stamp(s.GetCreatedAt())},
			{"updated", stamp(s.GetUpdatedAt())},
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
	fs := e.flags("slots evict")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu slots evict <name|id>")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.slots.EvictSlot(ctx, connect.NewRequest(&v1.EvictSlotRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { slotsTable(w, []*v1.Slot{resp.Msg.GetSlot()}) })
}

func runSlotsRemove(ctx context.Context, e *env, args []string) error {
	fs := e.flags("slots remove")
	force := fs.Bool("force", false, "stop the occupant first")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu slots remove <name|id> [--force]")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.slots.DeleteSlot(ctx, connect.NewRequest(&v1.DeleteSlotRequest{Id: positional[0], Force: *force}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "removed %s\n", resp.Msg.GetSlot().GetName()) })
}

func runSwap(ctx context.Context, e *env, args []string) error {
	fs := e.flags("swap")
	source := fs.String("source", "", "source id, first configured when empty")
	group := fs.String("group", "", "weight group, required when several are stored")
	runtimeID := fs.String("runtime", "", "runtime id, the slot default or the first that accepts the format when empty")
	installID := fs.String("install", "", "install id, newest for the runtime when empty")
	drainFirst := fs.Bool("drain-first", false, "stop the old instance before starting the new one even when both fit")
	profile := fs.String("profile", "", "profile id or name to start params from, the runtime default when empty")
	var params multi
	fs.Var(&params, "param", "runtime param as name=value, repeatable, over the profile and slot defaults")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 2 {
		return fmt.Errorf("usage: nebu swap <slot> <repo> [flags]")
	}
	paramMap, err := parseParams(params)
	if err != nil {
		return err
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
	groupName, err := e.onlyGroup(ctx, cl, sourceID, positional[1], *group)
	if err != nil {
		return err
	}
	run := &v1.RunRequest{SourceId: sourceID, Repo: positional[1], Group: groupName, RuntimeId: *runtimeID, InstallId: *installID, Params: paramMap, ProfileId: *profile}
	resp, err := cl.slots.Swap(ctx, connect.NewRequest(&v1.SwapRequest{SlotId: positional[0], Run: run, DrainFirst: *drainFirst}))
	if err != nil {
		return err
	}
	if e.json {
		if _, err := e.watchSilently(ctx, cl, resp.Msg.GetTask().GetId()); err != nil {
			return err
		}
		final, err := cl.slots.GetSlot(ctx, connect.NewRequest(&v1.GetSlotRequest{Id: resp.Msg.GetSlot().GetId()}))
		if err != nil {
			return err
		}
		return e.print(final.Msg, nil)
	}
	fmt.Fprintf(e.out, "slot %s %s\n", resp.Msg.GetSlot().GetName(), strings.ToLower(eval.EnumShort(resp.Msg.GetSlot().GetState())))
	if _, err := e.watch(ctx, cl, resp.Msg.GetTask().GetId()); err != nil {
		return err
	}
	final, err := cl.slots.GetSlot(ctx, connect.NewRequest(&v1.GetSlotRequest{Id: resp.Msg.GetSlot().GetId()}))
	if err != nil {
		return err
	}
	s := final.Msg.GetSlot()
	if s.GetState() != v1.SlotState_SLOT_STATE_READY {
		return fmt.Errorf("slot %s is %s: %s", s.GetName(), eval.EnumShort(s.GetState()), s.GetError())
	}
	fmt.Fprintf(e.out, "gateway %s/v1 model %s serves %s\n", e.gatewayBase(), s.GetName(), modelText(s.GetRequest()))
	return nil
}

func runRoutesList(ctx context.Context, e *env, args []string) error {
	fs := e.flags("routes list")
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
	resp, err := cl.gateway.ListRoutes(ctx, connect.NewRequest(&v1.ListRoutesRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { routesTable(w, resp.Msg.GetRoutes()) })
}

func routesTable(w io.Writer, list []*v1.Route) {
	var rows [][]string
	for _, r := range list {
		rows = append(rows, []string{r.GetName(), strings.ToUpper(eval.EnumShort(r.GetState())), r.GetModel(), r.GetInstanceId(), r.GetSlotId(), r.GetEndpoint(), strconv.FormatUint(r.GetRequests(), 10), strconv.Itoa(int(r.GetInFlight())), policyText(r.GetPolicy())})
	}
	table(w, []string{"NAME", "STATE", "MODEL", "INSTANCE", "SLOT", "ENDPOINT", "REQUESTS", "IN FLIGHT", "LIMITS"}, rows)
}

func runRoutesAdd(ctx context.Context, e *env, args []string) error {
	fs := e.flags("routes add")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 2 {
		return fmt.Errorf("usage: nebu routes add <name> <instance>")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	in, err := cl.instances.GetInstance(ctx, connect.NewRequest(&v1.GetInstanceRequest{Id: positional[1]}))
	if err != nil {
		return err
	}
	resp, err := cl.gateway.SetRoute(ctx, connect.NewRequest(&v1.SetRouteRequest{Name: positional[0], InstanceId: in.Msg.GetInstance().GetId()}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { routesTable(w, []*v1.Route{resp.Msg.GetRoute()}) })
}

func runRoutesRemove(ctx context.Context, e *env, args []string) error {
	fs := e.flags("routes remove")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu routes remove <name>")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.gateway.DeleteRoute(ctx, connect.NewRequest(&v1.DeleteRouteRequest{Name: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "removed %s\n", resp.Msg.GetRoute().GetName()) })
}

func runGateway(ctx context.Context, e *env, args []string) error {
	fs := e.flags("gateway")
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
	resp, err := cl.gateway.GetGatewayStatus(ctx, connect.NewRequest(&v1.GetGatewayStatusRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		st := resp.Msg.GetStatus()
		for _, l := range st.GetListeners() {
			shared := ""
			if l.GetShared() {
				shared = " shared with the api"
			}
			scheme := "http"
			if st.GetTls() {
				scheme = "https"
			}
			fmt.Fprintf(w, "listening %s://%s/v1 and /api%s\n", scheme, l.GetAddr(), shared)
		}
		fmt.Fprintf(w, "auth %t tls %t requests %d default limits %s\n", st.GetAuth(), st.GetTls(), st.GetRequests(), policyText(st.GetPolicy()))
		section(w, "routes")
		routesTable(w, st.GetRoutes())
	})
}

func runMonitorList(ctx context.Context, e *env, args []string) error {
	fs := e.flags("monitor list")
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
	resp, err := cl.monitor.ListWatches(ctx, connect.NewRequest(&v1.ListWatchesRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { watchesTable(w, resp.Msg.GetWatches()) })
}

func watchesTable(w io.Writer, list []*v1.Watch) {
	var rows [][]string
	for _, wt := range list {
		auto := "-"
		if wt.GetAutoPull() {
			auto = "pull"
			if wt.GetSlotId() != "" {
				auto = "pull+swap"
			}
		}
		rows = append(rows, []string{wt.GetId(), wt.GetSourceId(), wt.GetRepo(), wt.GetRevision(), shortCommit(wt.GetLastCommit()), strconv.Itoa(len(wt.GetKnownGroups())), wt.GetGroupMatch(), auto, wt.GetSlotId(), stamp(wt.GetCheckedAt()), wt.GetError()})
	}
	table(w, []string{"ID", "SOURCE", "REPO", "REVISION", "COMMIT", "GROUPS", "MATCH", "AUTO", "SLOT", "CHECKED", "ERROR"}, rows)
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	if c == "" {
		return "-"
	}
	return c
}

func runMonitorAdd(ctx context.Context, e *env, args []string) error {
	fs := e.flags("monitor add")
	source := fs.String("source", "", "source id, first configured when empty")
	revision := fs.String("revision", "", "branch or tag, the source default when empty")
	match := fs.String("match", "", "regex over weight group names that auto pull applies to")
	autoPull := fs.Bool("auto-pull", false, "pull matching groups when they appear or change")
	slot := fs.String("slot", "", "slot to swap onto the freshest pull, implies --auto-pull")
	runtimeID := fs.String("runtime", "", "runtime used when swapping")
	profile := fs.String("profile", "", "profile id or name the swap starts params from")
	var params multi
	fs.Var(&params, "param", "runtime param used when swapping, repeatable")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu monitor add <repo> [flags]")
	}
	paramMap, err := parseParams(params)
	if err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.monitor.AddWatch(ctx, connect.NewRequest(&v1.AddWatchRequest{SourceId: *source, Repo: positional[0], Revision: *revision, GroupMatch: *match, AutoPull: *autoPull || *slot != "", SlotId: *slot, RuntimeId: *runtimeID, Params: paramMap, ProfileId: *profile}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { watchesTable(w, []*v1.Watch{resp.Msg.GetWatch()}) })
}

func wantsTable(w io.Writer, list []*v1.Want) {
	var rows [][]string
	for _, wt := range list {
		where := "every source"
		if wt.GetSourceId() != "" {
			where = wt.GetSourceId()
		} else if wt.GetKind() != v1.SourceKind_SOURCE_KIND_UNSPECIFIED {
			where = eval.EnumShort(wt.GetKind())
		}
		on := "record"
		if wt.GetAutoPull() {
			on = "pull"
			if wt.GetSlotId() != "" {
				on = "pull+swap " + wt.GetSlotId()
			}
		}
		found := "-"
		if wt.GetSatisfied() {
			found = wt.GetFoundSourceId() + " " + wt.GetFoundRepo() + " " + wt.GetFoundGroup()
		}
		rows = append(rows, []string{wt.GetId(), wt.GetQuery(), where, wt.GetGroupMatch(), wt.GetFormatId(), on, found, stamp(wt.GetCheckedAt()), wt.GetError()})
	}
	table(w, []string{"ID", "QUERY", "WHERE", "MATCH", "FORMAT", "ON FOUND", "FOUND", "CHECKED", "ERROR"}, rows)
}

func runMonitorWants(ctx context.Context, e *env, args []string) error {
	fs := e.flags("monitor wants")
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
	resp, err := cl.monitor.ListWants(ctx, connect.NewRequest(&v1.ListWantsRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { wantsTable(w, resp.Msg.GetWants()) })
}

func runMonitorWant(ctx context.Context, e *env, args []string) error {
	fs := e.flags("monitor want")
	source := fs.String("source", "", "only this source, every source when empty")
	kind := fs.String("kind", "", "only this provider, one of "+strings.Join(sourceKinds(), ", "))
	match := fs.String("match", "", "regex over weight group names that satisfy the want")
	format := fs.String("format", "", "format id a group must have, such as gguf")
	autoPull := fs.Bool("auto-pull", false, "pull the group once found")
	slot := fs.String("slot", "", "slot to swap onto the pull, implies --auto-pull")
	runtimeID := fs.String("runtime", "", "runtime used when swapping")
	profile := fs.String("profile", "", "profile id or name the swap starts params from")
	var params multi
	fs.Var(&params, "param", "runtime param used when swapping, repeatable")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		return fmt.Errorf("usage: nebu monitor want <query words> [flags]")
	}
	paramMap, err := parseParams(params)
	if err != nil {
		return err
	}
	req := &v1.AddWantRequest{Query: strings.Join(positional, " "), SourceId: *source, GroupMatch: *match, FormatId: *format, AutoPull: *autoPull, SlotId: *slot, RuntimeId: *runtimeID, Params: paramMap, ProfileId: *profile}
	if *kind != "" {
		if req.Kind, err = parseSourceKind(*kind); err != nil {
			return err
		}
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.monitor.AddWant(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { wantsTable(w, []*v1.Want{resp.Msg.GetWant()}) })
}

func runMonitorUnwant(ctx context.Context, e *env, args []string) error {
	fs := e.flags("monitor unwant")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu monitor unwant <id>")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.monitor.RemoveWant(ctx, connect.NewRequest(&v1.RemoveWantRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "no longer wanting %q\n", resp.Msg.GetWant().GetQuery()) })
}

func runMonitorRemove(ctx context.Context, e *env, args []string) error {
	fs := e.flags("monitor remove")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu monitor remove <id|repo>")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.monitor.RemoveWatch(ctx, connect.NewRequest(&v1.RemoveWatchRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "removed %s %s\n", resp.Msg.GetWatch().GetId(), resp.Msg.GetWatch().GetRepo())
	})
}

func runMonitorCheck(ctx context.Context, e *env, args []string) error {
	fs := e.flags("monitor check")
	rearm := fs.Bool("rearm", false, "look again for a satisfied want, forgetting what it found")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	req := &v1.CheckWatchesRequest{Rearm: *rearm}
	if len(positional) == 1 {
		req.Id = positional[0]
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.monitor.CheckWatches(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.follow(ctx, cl, resp.Msg.GetTask().GetId())
}

func runMonitorFindings(ctx context.Context, e *env, args []string) error {
	fs := e.flags("monitor findings")
	unacked := fs.Bool("unacked", false, "only findings not yet acknowledged")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	req := &v1.ListFindingsRequest{UnacknowledgedOnly: *unacked}
	if len(positional) == 1 {
		req.WatchId = positional[0]
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.monitor.ListFindings(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, f := range resp.Msg.GetFindings() {
			acked := "no"
			if f.GetAcknowledged() {
				acked = "yes"
			}
			rows = append(rows, []string{f.GetId(), strings.ToUpper(eval.EnumShort(f.GetKind())), f.GetRepo(), f.GetGroup(), shortCommit(f.GetCommit()), stamp(f.GetFoundAt()), f.GetTaskId(), acked, f.GetDetail()})
		}
		table(w, []string{"ID", "KIND", "REPO", "GROUP", "COMMIT", "FOUND", "TASK", "ACKED", "DETAIL"}, rows)
	})
}

func runMonitorAck(ctx context.Context, e *env, args []string) error {
	fs := e.flags("monitor ack")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu monitor ack <finding id>")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	resp, err := cl.monitor.AckFinding(ctx, connect.NewRequest(&v1.AckFindingRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "acknowledged %s\n", resp.Msg.GetFinding().GetId()) })
}

func runEvents(ctx context.Context, e *env, args []string) error {
	fs := e.flags("events")
	snapshot := fs.Bool("snapshot", false, "send the current state first")
	var kinds multi
	fs.Var(&kinds, "kind", "event kind such as instance, task, slot, route, repeatable")
	if _, err := parse(fs, args); err != nil {
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
	cl, err := e.clients()
	if err != nil {
		return err
	}
	if err := e.requireDaemon(); err != nil {
		return err
	}
	stream, err := cl.events.WatchEvents(ctx, connect.NewRequest(req))
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
