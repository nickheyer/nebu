package cli

import (
	"context"
	"fmt"
	"io"
	"runtime/debug"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/nickheyer/nebu/internal/daemon"
	"github.com/nickheyer/nebu/pkg/estimate"
	"github.com/nickheyer/nebu/pkg/eval"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
)

func runServe(ctx context.Context, e *env, args []string) error {
	fs := e.flags("serve")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	d, err := daemon.New(e.cfg, e.log)
	if err != nil {
		return err
	}
	return d.ListenAndServe(ctx)
}

func runDoctor(ctx context.Context, e *env, args []string) error {
	fs := e.flags("doctor")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.host.Doctor(ctx, connect.NewRequest(&v1.DoctorRequest{}))
	if err != nil {
		return err
	}
	failed := 0
	err = e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, c := range resp.Msg.GetReport().GetChecks() {
			rows = append(rows, []string{strings.ToUpper(eval.EnumShort(c.GetStatus())), c.GetId(), c.GetSummary(), c.GetHint()})
		}
		table(w, []string{"STATUS", "CHECK", "SUMMARY", "HINT"}, rows)
	})
	for _, c := range resp.Msg.GetReport().GetChecks() {
		if c.GetStatus() == v1.CheckStatus_CHECK_STATUS_FAIL {
			failed++
		}
	}
	if err == nil && failed > 0 {
		return fmt.Errorf("%d checks failed", failed)
	}
	return err
}

func runHost(ctx context.Context, e *env, args []string) error {
	fs := e.flags("host")
	refresh := fs.Bool("refresh", false, "probe again instead of using the cached profile")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.host.GetProfile(ctx, connect.NewRequest(&v1.GetProfileRequest{Refresh: *refresh}))
	if err != nil {
		return err
	}
	p := resp.Msg.GetProfile()
	return e.print(resp.Msg, func(w io.Writer) {
		fmt.Fprintf(w, "%s %s/%s\n", p.GetHostname(), p.GetOs(), p.GetArch())
		section(w, "devices")
		var rows [][]string
		for _, d := range p.GetDevices() {
			rows = append(rows, []string{d.GetId(), eval.EnumShort(d.GetKind()), d.GetVendor(), d.GetName(), estimate.Human(d.GetMemoryTotalBytes()), compact(d.GetFacts())})
		}
		table(w, []string{"ID", "KIND", "VENDOR", "NAME", "MEMORY", "FACTS"}, rows)
		section(w, "pools")
		rows = nil
		for _, pl := range p.GetPools() {
			rows = append(rows, []string{pl.GetId(), eval.EnumShort(pl.GetKind()), pl.GetDeviceId(), estimate.Human(pl.GetTotalBytes()), estimate.Human(pl.GetFreeBytes())})
		}
		table(w, []string{"ID", "KIND", "DEVICE", "TOTAL", "FREE"}, rows)
		section(w, "storage")
		rows = nil
		for _, st := range p.GetStorage() {
			rows = append(rows, []string{st.GetPath(), st.GetFilesystem(), estimate.Human(st.GetTotalBytes()), estimate.Human(st.GetFreeBytes()), strings.Join(st.GetUses(), " ")})
		}
		table(w, []string{"MOUNT", "FS", "TOTAL", "FREE", "USED BY"}, rows)
		section(w, "probes")
		rows = nil
		for _, pr := range p.GetProbes() {
			rows = append(rows, []string{pr.GetProbeId(), eval.EnumShort(pr.GetStatus()), pr.GetDetail()})
		}
		table(w, []string{"ID", "STATUS", "DETAIL"}, rows)
	})
}

func runSources(ctx context.Context, e *env, args []string) error {
	fs := e.flags("sources")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.sources.ListSources(ctx, connect.NewRequest(&v1.ListSourcesRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, st := range resp.Msg.GetSources() {
			s, c := st.GetSource(), st.GetCapabilities()
			var can []string
			for _, f := range []struct {
				ok   bool
				name string
			}{{c.GetBrowse(), "browse"}, {c.GetSearch(), "search"}, {c.GetRevisions(), "revisions"}, {c.GetCard(), "card"}} {
				if f.ok {
					can = append(can, f.name)
				}
			}
			auth := "-"
			switch {
			case c.GetTokenPresent():
				auth = "token set"
			case c.GetAuthRequired():
				auth = "token needed"
			}
			if auth != "-" && c.GetTokenEnv() != "" {
				auth += " (" + c.GetTokenEnv() + ")"
			}
			var facets []string
			for _, f := range c.GetFacets() {
				facets = append(facets, f.GetId())
			}
			where := c.GetEndpoint()
			if where == "" {
				where = c.GetWebUrl()
			}
			origin := "added"
			if s.GetSeeded() {
				origin = "seeded"
			}
			if st.GetError() != "" {
				can = []string{"error: " + st.GetError()}
			}
			rows = append(rows, []string{s.GetId(), s.GetName(), eval.EnumShort(s.GetKind()), origin, where, auth, strings.Join(can, ","), strings.Join(sortIDs(c), ","), strings.Join(facets, ",")})
		}
		table(w, []string{"ID", "NAME", "PROVIDER", "ORIGIN", "LOCATION", "AUTH", "CAN", "SORTS", "FACETS"}, rows)
	})
}

// Sort ids with a ± on the ones --asc can flip
func sortIDs(c *v1.SourceCapabilities) []string {
	var out []string
	for _, s := range c.GetSorts() {
		id := s.GetId()
		if s.GetReversible() {
			id += "±"
		}
		out = append(out, id)
	}
	return out
}

func runSearch(ctx context.Context, e *env, args []string) error {
	fs := e.flags("search")
	source := fs.String("source", "", "source id, every source when empty")
	kind := fs.String("kind", "", "provider, every source of it merged, one of "+strings.Join(sourceKinds(), ", "))
	limit := fs.Uint("limit", 20, "maximum hits per page")
	sortBy := fs.String("sort", "", "sort id, see nebu sources for what each source accepts")
	asc := fs.Bool("asc", false, "ascending instead of descending")
	author := fs.String("author", "", "only repositories by this owner")
	cursor := fs.String("cursor", "", "continue from the cursor a previous page printed")
	var tags, filters multi
	fs.Var(&tags, "tag", "tag filter, repeatable")
	fs.Var(&filters, "filter", "facet filter as facet=value, repeatable")
	query, err := parse(fs, args)
	if err != nil {
		return err
	}
	req := &v1.SearchRequest{
		SourceId:  *source,
		Query:     strings.Join(query, " "),
		Tags:      tags,
		Limit:     uint32(*limit),
		Sort:      *sortBy,
		Ascending: *asc,
		Author:    *author,
		Cursor:    *cursor,
		Filters:   map[string]string{},
	}
	for _, f := range filters {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return fmt.Errorf("filter %q: expected facet=value", f)
		}
		if prev, dup := req.Filters[k]; dup {
			v = prev + "," + v
		}
		req.Filters[k] = v
	}
	if *kind != "" {
		if req.Kind, err = parseSourceKind(*kind); err != nil {
			return err
		}
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.sources.Search(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, h := range resp.Msg.GetHits() {
			updated := "-"
			if h.GetUpdatedAt() != nil {
				updated = h.GetUpdatedAt().AsTime().Format("2006-01-02")
			}
			size := "-"
			if h.GetParameters() > 0 {
				size = humanCount(h.GetParameters())
			} else if h.GetSizeBytes() > 0 {
				size = estimate.Human(h.GetSizeBytes())
			}
			rows = append(rows, []string{h.GetSourceId(), h.GetRepo(), h.GetTask(), size, strconv.FormatUint(h.GetDownloads(), 10), strconv.FormatUint(h.GetLikes(), 10), updated, strings.Join(h.GetFormats(), ",")})
		}
		table(w, []string{"SOURCE", "REPO", "TASK", "SIZE", "DOWNLOADS", "LIKES", "UPDATED", "FORMATS"}, rows)
		for _, warn := range resp.Msg.GetWarnings() {
			fmt.Fprintln(w, "warning:", warn)
		}
		var notes []string
		if resp.Msg.GetTotal() > 0 {
			notes = append(notes, fmt.Sprintf("%d matches", resp.Msg.GetTotal()))
		}
		if resp.Msg.GetNextCursor() != "" {
			notes = append(notes, fmt.Sprintf("next page: --cursor %q", resp.Msg.GetNextCursor()))
		}
		if len(notes) > 0 {
			fmt.Fprintln(w, "\n"+strings.Join(notes, ", "))
		}
	})
}

func runRevisions(ctx context.Context, e *env, args []string) error {
	fs := e.flags("revisions")
	source := fs.String("source", "", "source id, first configured when empty")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu revisions <repo> [--source S]")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.sources.ListRevisions(ctx, connect.NewRequest(&v1.ListRevisionsRequest{SourceId: *source, Repo: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, r := range resp.Msg.GetRevisions() {
			def := ""
			if r.GetDefault() {
				def = "*"
			}
			size := "-"
			if r.GetSizeBytes() > 0 {
				size = estimate.Human(r.GetSizeBytes())
			}
			updated := "-"
			if r.GetUpdatedAt() != nil {
				updated = r.GetUpdatedAt().AsTime().Format("2006-01-02")
			}
			rows = append(rows, []string{def, r.GetName(), r.GetRepo(), shortCommit(r.GetCommit()), size, updated, r.GetDetail()})
		}
		table(w, []string{"", "REVISION", "REPO", "COMMIT", "SIZE", "UPDATED", "DETAIL"}, rows)
	})
}

func runCard(ctx context.Context, e *env, args []string) error {
	fs := e.flags("card")
	source := fs.String("source", "", "source id, first configured when empty")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu card <repo>[@revision] [--source S]")
	}
	repo, revision := splitRef(positional[0])
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.sources.GetModelCard(ctx, connect.NewRequest(&v1.GetModelCardRequest{SourceId: *source, Repo: repo, Revision: revision}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		card := resp.Msg.GetCard()
		switch {
		case card.GetMarkdown() != "":
			fmt.Fprintln(w, card.GetMarkdown())
		case card.GetHtml() != "":
			fmt.Fprintln(w, sources.StripTags(card.GetHtml()))
		default:
			fmt.Fprintln(w, "no card published")
		}
		if card.GetUrl() != "" {
			fmt.Fprintln(w, "\n"+card.GetUrl())
		}
	})
}

func runInspect(ctx context.Context, e *env, args []string) error {
	fs := e.flags("inspect")
	source := fs.String("source", "", "source id, first configured when empty")
	slot := fs.String("slot", "", "slot id or name to plan inside, its devices, budget, and defaults")
	profile := fs.String("profile", "", "profile id or name to start params from, plans its runtime alone unless --runtime says otherwise")
	var runtimes, groups, contexts, params multi
	fs.Var(&runtimes, "runtime", "runtime id, repeatable")
	fs.Var(&groups, "group", "weight group name, repeatable")
	fs.Var(&contexts, "ctx", "context length, repeatable")
	fs.Var(&params, "param", "runtime param as name=value, repeatable, over the profile and slot defaults")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu inspect <repo>[@revision] [flags]")
	}
	repo, revision := splitRef(positional[0])
	req := &v1.InspectRequest{SourceId: *source, Repo: repo, Revision: revision, Groups: groups, RuntimeIds: runtimes, Params: map[string]string{}, SlotId: *slot, ProfileId: *profile}
	for _, c := range contexts {
		n, err := strconv.ParseUint(c, 10, 32)
		if err != nil {
			return fmt.Errorf("ctx %q: %w", c, err)
		}
		req.Contexts = append(req.Contexts, uint32(n))
	}
	for _, p := range params {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return fmt.Errorf("param %q: expected name=value", p)
		}
		req.Params[k] = v
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.estimate.Inspect(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { renderInspect(w, resp.Msg) })
}

func renderInspect(w io.Writer, resp *v1.InspectResponse) {
	m := resp.GetModel()
	fmt.Fprintf(w, "%s@%s %s\n", m.GetRepo(), m.GetRevision(), m.GetCommit())
	section(w, "weight groups")
	var rows [][]string
	for _, d := range resp.GetDescriptors() {
		p := d.GetParams()
		rows = append(rows, []string{
			d.GetGroup(), d.GetFormatId(), d.GetArchitecture(),
			humanCount(d.GetParameterCount()), fmt.Sprintf("%.2f", d.GetBitsPerWeight()), estimate.Human(d.GetTotalBytes()),
			num(p["n_layer"]), num(p["n_ctx_train"]), num(p["n_expert"]),
		})
	}
	table(w, []string{"GROUP", "FORMAT", "ARCH", "PARAMS", "BPW", "WEIGHTS", "LAYERS", "CTX TRAIN", "EXPERTS"}, rows)
	section(w, "fit")
	rows = nil
	for _, r := range resp.GetRows() {
		plan := r.GetPlan()
		rows = append(rows, []string{
			r.GetGroup(), r.GetRuntimeId(), strconv.FormatUint(uint64(r.GetContext()), 10),
			strings.ToUpper(eval.EnumShort(plan.GetVerdict())), strings.ToUpper(eval.EnumShort(r.GetFree().GetVerdict())),
			poolUsage(plan, v1.PoolKind_POOL_KIND_DEVICE), poolUsage(plan, v1.PoolKind_POOL_KIND_HOST),
			estimate.Human(plan.GetCacheBytes()), placements(plan), plan.GetDetail(),
		})
	}
	table(w, []string{"GROUP", "RUNTIME", "CTX", "TOTAL", "FREE NOW", "DEVICE", "HOST", "CACHE", "PLACEMENT", "DETAIL"}, rows)
	for _, warn := range resp.GetWarnings() {
		fmt.Fprintln(w, "warning:", warn)
	}
}

func poolUsage(plan *v1.MemoryPlan, kind v1.PoolKind) string {
	var used, capacity uint64
	for _, p := range plan.GetPools() {
		if p.GetKind() == kind || (kind == v1.PoolKind_POOL_KIND_DEVICE && p.GetKind() == v1.PoolKind_POOL_KIND_UNIFIED) {
			used += p.GetUsedBytes()
			capacity += p.GetCapacityBytes()
		}
	}
	if capacity == 0 {
		return "-"
	}
	return fmt.Sprintf("%s/%s", estimate.Human(used), estimate.Human(capacity))
}

func placements(plan *v1.MemoryPlan) string {
	type tally struct{ device, host uint32 }
	counts := map[v1.TensorGroupKind]*tally{}
	var order []v1.TensorGroupKind
	for _, p := range plan.GetPlacements() {
		t, ok := counts[p.GetKind()]
		if !ok {
			t = &tally{}
			counts[p.GetKind()] = t
			order = append(order, p.GetKind())
		}
		if p.GetPoolId() == eval.EnumShort(v1.PoolKind_POOL_KIND_HOST) {
			t.host += p.GetCount()
		} else {
			t.device += p.GetCount()
		}
	}
	var parts []string
	for _, k := range order {
		t := counts[k]
		if t.device == 0 {
			parts = append(parts, eval.EnumShort(k)+":host")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d/%d", eval.EnumShort(k), t.device, t.device+t.host))
	}
	return strings.Join(parts, " ")
}

// Prints one runtime with its params, what --param and profiles may name
func runRuntimesShow(ctx context.Context, e *env, args []string) error {
	fs := e.flags("runtimes show")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: nebu runtimes show <runtime>")
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.runtimes.ListRuntimes(ctx, connect.NewRequest(&v1.ListRuntimesRequest{}))
	if err != nil {
		return err
	}
	var rt *v1.RuntimeStatus
	for _, r := range resp.Msg.GetRuntimes() {
		if r.GetManifest().GetId() == positional[0] {
			rt = r
		}
	}
	if rt == nil {
		return fmt.Errorf("unknown runtime %q", positional[0])
	}
	return e.print(rt, func(w io.Writer) {
		m := rt.GetManifest()
		for _, pair := range [][2]string{{"id", m.GetId()}, {"name", m.GetName()}, {"formats", strings.Join(m.GetFormats(), ", ")}, {"api", eval.EnumShort(m.GetLaunch().GetApi())}, {"compatible", strconv.FormatBool(rt.GetCompatible())}, {"unmet", strings.Join(rt.GetUnmet(), "; ")}} {
			fmt.Fprintf(w, "%-11s %s\n", pair[0], pair[1])
		}
		section(w, "params")
		var rows [][]string
		for _, p := range m.GetParams() {
			rows = append(rows, []string{p.GetName(), eval.EnumShort(p.GetType()), p.GetDefault(), strings.Join(p.GetChoices(), ","), p.GetDescription()})
		}
		table(w, []string{"NAME", "TYPE", "DEFAULT", "CHOICES", "DESCRIPTION"}, rows)
	})
}

func runRuntimes(ctx context.Context, e *env, args []string) error {
	fs := e.flags("runtimes")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cl, err := e.clients()
	if err != nil {
		return err
	}
	resp, err := cl.runtimes.ListRuntimes(ctx, connect.NewRequest(&v1.ListRuntimesRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, rt := range resp.Msg.GetRuntimes() {
			m := rt.GetManifest()
			rows = append(rows, []string{m.GetId(), m.GetName(), strings.Join(m.GetFormats(), ","), strconv.FormatBool(rt.GetCompatible()), strings.Join(rt.GetUnmet(), "; ")})
		}
		table(w, []string{"ID", "NAME", "FORMATS", "COMPATIBLE", "UNMET"}, rows)
	})
}

// Reads the module version and commit out of the binary
func buildVersion() string {
	version, revision := "devel", ""
	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				revision = s.Value[:7]
			}
		}
	}
	return strings.TrimSpace(version + " " + revision)
}

func runVersion(ctx context.Context, e *env, args []string) error {
	_, err := fmt.Fprintln(e.out, "nebu "+buildVersion())
	return err
}

func splitRef(ref string) (string, string) {
	if i := strings.LastIndex(ref, "@"); i > 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func num(f float64) string {
	if f == 0 {
		return "-"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func humanCount(n uint64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.0fM", float64(n)/1e6)
	}
	return strconv.FormatUint(n, 10)
}
