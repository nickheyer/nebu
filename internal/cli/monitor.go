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
	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

func runMonitorList(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("monitor list"), args, 0, 0, "monitor list"); err != nil {
		return err
	}
	resp, err := e.cl.monitor.ListWatches(ctx, connect.NewRequest(&v1.ListWatchesRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { watchesTable(w, resp.Msg.GetWatches()) })
}

func watchesTable(w io.Writer, list []*v1.Watch) {
	var rows [][]string
	for _, wt := range list {
		rows = append(rows, []string{wt.GetId(), wt.GetSourceId(), wt.GetRepo(), wt.GetRevision(), shortCommit(wt.GetLastCommit()), strconv.Itoa(len(wt.GetKnownGroups())), wt.GetGroupMatch(), onFound(wt.GetAutoPull(), wt.GetSlotId()), wt.GetSlotId(), when(wt.GetCheckedAt(), time.RFC3339), wt.GetError()})
	}
	table(w, []string{"ID", "SOURCE", "REPO", "REVISION", "COMMIT", "GROUPS", "MATCH", "AUTO", "SLOT", "CHECKED", "ERROR"}, rows)
}

// What a watch or want does with a match
func onFound(autoPull bool, slotID string) string {
	switch {
	case autoPull && slotID != "":
		return "pull+swap"
	case autoPull:
		return "pull"
	}
	return "-"
}

// Declares the flags a watch and a want share, the returned func reads them once parsed
func swapFlags(fs *flag.FlagSet) (*v1.AddWatchRequest, func() error) {
	req := &v1.AddWatchRequest{}
	var params multi
	fs.StringVar(&req.SourceId, "source", "", "source id, first configured when empty")
	fs.StringVar(&req.GroupMatch, "match", "", "regex over weight group names the action applies to")
	fs.BoolVar(&req.AutoPull, "auto-pull", false, "pull matching groups once found")
	fs.StringVar(&req.SlotId, "slot", "", "slot to swap onto the pull, implies --auto-pull")
	fs.StringVar(&req.RuntimeId, "runtime", "", "runtime used when swapping")
	fs.StringVar(&req.ProfileId, "profile", "", "profile id or name the swap starts params from")
	fs.Var(&params, "param", "runtime param used when swapping, repeatable")
	return req, func() error {
		var err error
		req.Params, err = pairs(params, "param")
		req.AutoPull = req.AutoPull || req.SlotId != ""
		return err
	}
}

func runMonitorAdd(ctx context.Context, e *env, args []string) error {
	fs := e.flags("monitor add")
	req, read := swapFlags(fs)
	fs.StringVar(&req.Revision, "revision", "", "branch or tag, the source default when empty")
	positional, err := e.parse(fs, args, 1, 1, "monitor add <repo> [flags]")
	if err != nil {
		return err
	}
	if err := read(); err != nil {
		return err
	}
	req.Repo = positional[0]
	resp, err := e.cl.monitor.AddWatch(ctx, connect.NewRequest(req))
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
			on = strings.TrimSpace(onFound(true, wt.GetSlotId()) + " " + wt.GetSlotId())
		}
		found := "-"
		if wt.GetSatisfied() {
			found = wt.GetFoundSourceId() + " " + wt.GetFoundRepo() + " " + wt.GetFoundGroup()
		}
		rows = append(rows, []string{wt.GetId(), wt.GetQuery(), where, wt.GetGroupMatch(), wt.GetFormatId(), on, found, when(wt.GetCheckedAt(), time.RFC3339), wt.GetError()})
	}
	table(w, []string{"ID", "QUERY", "WHERE", "MATCH", "FORMAT", "ON FOUND", "FOUND", "CHECKED", "ERROR"}, rows)
}

func runMonitorWants(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("monitor wants"), args, 0, 0, "monitor wants"); err != nil {
		return err
	}
	resp, err := e.cl.monitor.ListWants(ctx, connect.NewRequest(&v1.ListWantsRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { wantsTable(w, resp.Msg.GetWants()) })
}

func runMonitorWant(ctx context.Context, e *env, args []string) error {
	fs := e.flags("monitor want")
	watch, read := swapFlags(fs)
	kind := fs.String("kind", "", "only this provider, one of "+strings.Join(sourceKinds(), ", "))
	format := fs.String("format", "", "format id a group must have, such as gguf")
	positional, err := e.parse(fs, args, 1, -1, "monitor want <query words> [flags]")
	if err != nil {
		return err
	}
	if err := read(); err != nil {
		return err
	}
	req := &v1.AddWantRequest{Query: strings.Join(positional, " "), SourceId: watch.SourceId, GroupMatch: watch.GroupMatch, FormatId: *format, AutoPull: watch.AutoPull, SlotId: watch.SlotId, RuntimeId: watch.RuntimeId, Params: watch.Params, ProfileId: watch.ProfileId}
	if *kind != "" {
		if req.Kind, err = parseSourceKind(*kind); err != nil {
			return err
		}
	}
	resp, err := e.cl.monitor.AddWant(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { wantsTable(w, []*v1.Want{resp.Msg.GetWant()}) })
}

func runMonitorUnwant(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("monitor unwant"), args, 1, 1, "monitor unwant <id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.monitor.RemoveWant(ctx, connect.NewRequest(&v1.RemoveWantRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "no longer wanting %q\n", resp.Msg.GetWant().GetQuery()) })
}

func runMonitorRemove(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("monitor remove"), args, 1, 1, "monitor remove <id|repo>")
	if err != nil {
		return err
	}
	resp, err := e.cl.monitor.RemoveWatch(ctx, connect.NewRequest(&v1.RemoveWatchRequest{Id: positional[0]}))
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
	positional, err := e.parse(fs, args, 0, 1, "monitor check [id] [--rearm]")
	if err != nil {
		return err
	}
	req := &v1.CheckWatchesRequest{Rearm: *rearm}
	if len(positional) == 1 {
		req.Id = positional[0]
	}
	resp, err := e.cl.monitor.CheckWatches(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.done(e.follow(ctx, resp.Msg.GetTask().GetId()))
}

func runMonitorFindings(ctx context.Context, e *env, args []string) error {
	fs := e.flags("monitor findings")
	unacked := fs.Bool("unacked", false, "only findings not yet acknowledged")
	positional, err := e.parse(fs, args, 0, 1, "monitor findings [watch|want] [--unacked]")
	if err != nil {
		return err
	}
	req := &v1.ListFindingsRequest{UnacknowledgedOnly: *unacked}
	if len(positional) == 1 {
		req.WatchId = positional[0]
	}
	resp, err := e.cl.monitor.ListFindings(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, f := range resp.Msg.GetFindings() {
			rows = append(rows, []string{f.GetId(), loud(f.GetKind()), f.GetRepo(), f.GetGroup(), shortCommit(f.GetCommit()), when(f.GetFoundAt(), time.RFC3339), f.GetTaskId(), orDash(f.GetSwapTaskId()), yes(f.GetAcknowledged()), f.GetDetail()})
		}
		table(w, []string{"ID", "KIND", "REPO", "GROUP", "COMMIT", "FOUND", "PULL", "SWAP", "ACKED", "DETAIL"}, rows)
	})
}

func runMonitorAck(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("monitor ack"), args, 1, 1, "monitor ack <finding id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.monitor.AckFinding(ctx, connect.NewRequest(&v1.AckFindingRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "acknowledged %s\n", resp.Msg.GetFinding().GetId()) })
}
