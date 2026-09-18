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
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/text"
)

func runDoctor(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("doctor"), args, 0, 0, "doctor"); err != nil {
		return err
	}
	resp, err := e.cl.host.Doctor(ctx, connect.NewRequest(&v1.DoctorRequest{}))
	if err != nil {
		return err
	}
	return e.done(e.follow(ctx, resp.Msg.GetTask().GetId()))
}

func runHost(ctx context.Context, e *env, args []string) error {
	fs := e.flags("host")
	refresh := fs.Bool("refresh", false, "probe again instead of using the cached profile")
	label := fs.String("label", "", "what to call this host, the hostname when cleared with an empty value")
	if _, err := e.parse(fs, args, 0, 0, "host [--refresh] [--label NAME]"); err != nil {
		return err
	}
	// Naming the flag, even empty, sets the label, leaving it out only reads
	labelSet := false
	fs.Visit(func(f *flag.Flag) { labelSet = labelSet || f.Name == "label" })
	current, err := e.cl.settings.GetSettings(ctx, connect.NewRequest(&v1.GetSettingsRequest{}))
	if err != nil {
		return err
	}
	settings := current.Msg.GetSettings()
	if labelSet {
		settings.HostLabel = *label
		saved, err := e.cl.settings.UpdateSettings(ctx, connect.NewRequest(&v1.UpdateSettingsRequest{Settings: settings}))
		if err != nil {
			return err
		}
		settings = saved.Msg.GetSettings()
	}
	resp, err := e.cl.host.GetProfile(ctx, connect.NewRequest(&v1.GetProfileRequest{Refresh: *refresh}))
	if err != nil {
		return err
	}
	p := resp.Msg.GetProfile()
	return e.print(resp.Msg, func(w io.Writer) {
		if l := settings.GetHostLabel(); l != "" {
			fmt.Fprintf(w, "%s (%s) %s/%s\n", l, p.GetHostname(), p.GetOs(), p.GetArch())
		} else {
			fmt.Fprintf(w, "%s %s/%s\n", p.GetHostname(), p.GetOs(), p.GetArch())
		}
		section(w, "devices")
		var rows [][]string
		for _, d := range p.GetDevices() {
			rows = append(rows, []string{d.GetId(), text.Enum(d.GetKind()), d.GetVendor(), d.GetName(), estimate.Human(d.GetMemoryTotalBytes()), compact(d.GetFacts())})
		}
		table(w, []string{"ID", "KIND", "VENDOR", "NAME", "MEMORY", "FACTS"}, rows)
		section(w, "pools")
		rows = nil
		for _, pl := range p.GetPools() {
			rows = append(rows, []string{pl.GetId(), text.Enum(pl.GetKind()), pl.GetDeviceId(), estimate.Human(pl.GetTotalBytes()), estimate.Human(pl.GetFreeBytes())})
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
			rows = append(rows, []string{pr.GetProbeId(), text.Enum(pr.GetStatus()), pr.GetDetail()})
		}
		table(w, []string{"ID", "STATUS", "DETAIL"}, rows)
	})
}

// Reads a kind as its short name, such as huggingface or oci, or the full enum name
func parseSourceKind(s string) (v1.SourceKind, error) {
	name := strings.ToUpper(strings.TrimSpace(s))
	if !strings.HasPrefix(name, "SOURCE_KIND_") {
		name = "SOURCE_KIND_" + name
	}
	n, ok := v1.SourceKind_value[name]
	if !ok || n == 0 {
		return 0, fmt.Errorf("unknown provider %q, one of %s", s, strings.Join(sourceKinds(), ", "))
	}
	return v1.SourceKind(n), nil
}

// Every kind by short name, in enum order
func sourceKinds() []string {
	values := v1.SourceKind(0).Descriptor().Values()
	out := make([]string, 0, values.Len())
	for i := 1; i < values.Len(); i++ {
		out = append(out, text.Enum(v1.SourceKind(values.Get(i).Number())))
	}
	return out
}

func runSources(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("sources"), args, 0, 0, "sources"); err != nil {
		return err
	}
	resp, err := e.cl.sources.ListSources(ctx, connect.NewRequest(&v1.ListSourcesRequest{}))
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
			var facets, sorts []string
			for _, f := range c.GetFacets() {
				facets = append(facets, f.GetId())
			}
			// A ± marks the sorts --asc can flip
			for _, s := range c.GetSorts() {
				id := s.GetId()
				if s.GetReversible() {
					id += "±"
				}
				sorts = append(sorts, id)
			}
			if st.GetError() != "" {
				can = []string{"error: " + st.GetError()}
			}
			rows = append(rows, []string{s.GetId(), s.GetName(), text.Enum(s.GetKind()), origin(s.GetSeeded()), orDash(strings.TrimSpace(c.GetEndpoint() + c.GetWebUrl())), auth, strings.Join(can, ","), strings.Join(sorts, ","), strings.Join(facets, ",")})
		}
		table(w, []string{"ID", "NAME", "PROVIDER", "ORIGIN", "LOCATION", "AUTH", "CAN", "SORTS", "FACETS"}, rows)
	})
}

func origin(seeded bool) string {
	if seeded {
		return "seeded"
	}
	return "added"
}

func sourceRow(w io.Writer, st *v1.SourceStatus) {
	s, c := st.GetSource(), st.GetCapabilities()
	rows := [][]string{
		{"id", s.GetId()},
		{"name", s.GetName()},
		{"provider", c.GetName() + " (" + text.Enum(s.GetKind()) + ")"},
		{"origin", origin(s.GetSeeded())},
		{"transports", strings.Join(c.GetTransports(), ", ")},
		{"created", when(s.GetCreatedAt(), time.RFC3339)},
		{"updated", when(s.GetUpdatedAt(), time.RFC3339)},
	}
	for _, f := range c.GetFields() {
		value, set := s.GetConfig()[f.GetName()]
		if !set {
			value = orDash(f.GetDefault())
			if value != "-" {
				value += " (default)"
			}
		}
		rows = append(rows, []string{f.GetName(), value})
	}
	if st.GetError() != "" {
		rows = append(rows, []string{"error", st.GetError()})
	}
	table(w, nil, rows)
}

func runSourcesProviders(ctx context.Context, e *env, args []string) error {
	if _, err := e.parse(e.flags("sources providers"), args, 0, 0, "sources providers"); err != nil {
		return err
	}
	resp, err := e.cl.sources.ListProviders(ctx, connect.NewRequest(&v1.ListProvidersRequest{}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, p := range resp.Msg.GetProviders() {
			var settings []string
			for _, f := range p.GetFields() {
				s := f.GetName()
				if f.GetRequired() {
					s += "*"
				}
				if f.GetDefault() != "" {
					s += "=" + f.GetDefault()
				}
				settings = append(settings, s)
			}
			seeded := "seeded"
			if p.GetConfigured() {
				seeded = "config only"
			}
			rows = append(rows, []string{text.Enum(p.GetKind()), p.GetName(), seeded, strings.Join(p.GetTransports(), ","), strings.Join(settings, " ")})
		}
		table(w, []string{"KIND", "PROVIDER", "DEFAULT", "TRANSPORTS", "SETTINGS"}, rows)
	})
}

func runSourcesAdd(ctx context.Context, e *env, args []string) error {
	fs := e.flags("sources add")
	kind := fs.String("kind", "", "provider, one of "+strings.Join(sourceKinds(), ", "))
	name := fs.String("name", "", "display name, the id when empty")
	var settings multi
	fs.Var(&settings, "set", "setting as name=value, repeatable, see nebu sources providers")
	usage := "sources add <id> --kind KIND [--name NAME] [--set name=value]..."
	positional, err := e.parse(fs, args, 1, 1, usage)
	if err != nil {
		return err
	}
	if *kind == "" {
		return fmt.Errorf("usage: nebu %s", usage)
	}
	k, err := parseSourceKind(*kind)
	if err != nil {
		return err
	}
	cfg, err := pairs(settings, "setting")
	if err != nil {
		return err
	}
	src := &v1.Source{Id: positional[0], Kind: k, Name: *name, Config: cfg}
	resp, err := e.cl.sources.CreateSource(ctx, connect.NewRequest(&v1.CreateSourceRequest{Source: src}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { sourceRow(w, resp.Msg.GetSource()) })
}

func runSourcesUpdate(ctx context.Context, e *env, args []string) error {
	fs := e.flags("sources update")
	name := fs.String("name", "", "display name")
	var settings, unset multi
	fs.Var(&settings, "set", "setting as name=value, repeatable, merged into what the source has")
	fs.Var(&unset, "unset", "setting to drop back to the provider default, repeatable")
	positional, err := e.parse(fs, args, 1, 1, "sources update <id> [--name NAME] [--set name=value]... [--unset name]...")
	if err != nil {
		return err
	}
	cfg, err := pairs(settings, "setting")
	if err != nil {
		return err
	}
	current, err := e.findSource(ctx, positional[0])
	if err != nil {
		return err
	}
	// The update replaces every setting, so start from what the source has and change only what was passed
	src := current.GetSource()
	if src.Config == nil {
		src.Config = map[string]string{}
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "name" {
			src.Name = *name
		}
	})
	for k, v := range cfg {
		src.Config[k] = v
	}
	for _, k := range unset {
		delete(src.Config, k)
	}
	resp, err := e.cl.sources.UpdateSource(ctx, connect.NewRequest(&v1.UpdateSourceRequest{Source: src}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { sourceRow(w, resp.Msg.GetSource()) })
}

func runSourcesRemove(ctx context.Context, e *env, args []string) error {
	positional, err := e.parse(e.flags("sources remove"), args, 1, 1, "sources remove <id>")
	if err != nil {
		return err
	}
	resp, err := e.cl.sources.DeleteSource(ctx, connect.NewRequest(&v1.DeleteSourceRequest{Id: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) { fmt.Fprintf(w, "removed %s\n", resp.Msg.GetSource().GetId()) })
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
	runtimeID := fs.String("runtime", "", "only models a runtime serves, by id, see nebu runtimes")
	var tags, filters, formats multi
	fs.Var(&tags, "tag", "tag filter, repeatable")
	fs.Var(&filters, "filter", "facet filter as facet=value, repeatable")
	fs.Var(&formats, "format", "only models held in a format, repeatable: gguf, safetensors, diffusion, nemo, or nemo2")
	query, err := e.parse(fs, args, 0, -1, "search [query words] [flags]")
	if err != nil {
		return err
	}
	req := &v1.SearchRequest{SourceId: *source, Query: strings.Join(query, " "), Tags: tags, Limit: uint32(*limit), Sort: *sortBy, Ascending: *asc, Author: *author, Cursor: *cursor, Filters: map[string]string{}}
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
	if *runtimeID != "" {
		req.Filters[sources.FacetRuntime] = *runtimeID
	}
	if len(formats) > 0 {
		req.Filters[sources.FacetFormat] = strings.Join(formats, ",")
	}
	names, err := e.runtimeNames(ctx)
	if err != nil {
		return err
	}
	if *kind != "" {
		if req.Kind, err = parseSourceKind(*kind); err != nil {
			return err
		}
	}
	resp, err := e.cl.sources.Search(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, h := range resp.Msg.GetHits() {
			size := "-"
			if h.GetParameters() > 0 {
				size = humanCount(h.GetParameters())
			} else if h.GetSizeBytes() > 0 {
				size = estimate.Human(h.GetSizeBytes())
			}
			rows = append(rows, []string{h.GetSourceId(), h.GetRepo(), h.GetTask(), kindWord(h.GetKind()), size, strconv.FormatUint(h.GetDownloads(), 10), strconv.FormatUint(h.GetLikes(), 10), when(h.GetUpdatedAt(), time.DateOnly), strings.Join(h.GetFormats(), ","), names.of(h.GetRuntimes())})
		}
		table(w, []string{"SOURCE", "REPO", "TASK", "KIND", "SIZE", "DOWNLOADS", "LIKES", "UPDATED", "FORMATS", "RUNS ON"}, rows)
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

// A model's kind in a word, blank when the catalog did not say
func kindWord(k v1.ModelKind) string {
	if k == v1.ModelKind_MODEL_KIND_UNSPECIFIED {
		return "-"
	}
	return text.Enum(k)
}

// The runtimes by the bit each takes in a bitmask, read once from the daemon
type runtimeNames map[uint32]string

func (e *env) runtimeNames(ctx context.Context) (runtimeNames, error) {
	resp, err := e.cl.runtimes.ListRuntimes(ctx, connect.NewRequest(&v1.ListRuntimesRequest{}))
	if err != nil {
		return nil, err
	}
	out := runtimeNames{}
	for _, rt := range resp.Msg.GetRuntimes() {
		out[rt.GetRuntime().GetBit()] = rt.GetRuntime().GetId()
	}
	return out, nil
}

// The runtimes a bitmask names, joined by commas, a dash for none
func (n runtimeNames) of(mask uint32) string {
	var out []string
	for bit := uint32(1); bit != 0 && bit <= mask; bit <<= 1 {
		if mask&bit != 0 {
			if id, ok := n[bit]; ok {
				out = append(out, id)
			}
		}
	}
	return orDash(strings.Join(out, ","))
}

func runRevisions(ctx context.Context, e *env, args []string) error {
	fs := e.flags("revisions")
	source := fs.String("source", "", "source id, first configured when empty")
	positional, err := e.parse(fs, args, 1, 1, "revisions <repo> [--source S]")
	if err != nil {
		return err
	}
	resp, err := e.cl.sources.ListRevisions(ctx, connect.NewRequest(&v1.ListRevisionsRequest{SourceId: *source, Repo: positional[0]}))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		var rows [][]string
		for _, r := range resp.Msg.GetRevisions() {
			def, size := "", "-"
			if r.GetDefault() {
				def = "*"
			}
			if r.GetSizeBytes() > 0 {
				size = estimate.Human(r.GetSizeBytes())
			}
			rows = append(rows, []string{def, r.GetName(), r.GetRepo(), shortCommit(r.GetCommit()), size, when(r.GetUpdatedAt(), time.DateOnly), r.GetDetail()})
		}
		table(w, []string{"", "REVISION", "REPO", "COMMIT", "SIZE", "UPDATED", "DETAIL"}, rows)
	})
}

func runCard(ctx context.Context, e *env, args []string) error {
	fs := e.flags("card")
	source := fs.String("source", "", "source id, first configured when empty")
	positional, err := e.parse(fs, args, 1, 1, "card <repo>[@revision] [--source S]")
	if err != nil {
		return err
	}
	repo, revision := splitRef(positional[0])
	resp, err := e.cl.sources.GetModelCard(ctx, connect.NewRequest(&v1.GetModelCardRequest{SourceId: *source, Repo: repo, Revision: revision}))
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
	var runtimes, groups, contexts, params multi
	fs.Var(&runtimes, "runtime", "runtime id, repeatable")
	fs.Var(&groups, "group", "weight group name, repeatable")
	fs.Var(&contexts, "ctx", "context length, repeatable")
	fs.Var(&params, "param", "runtime param as name=value, repeatable, over the slot defaults")
	positional, err := e.parse(fs, args, 1, 1, "inspect <repo>[@revision] [flags]")
	if err != nil {
		return err
	}
	repo, revision := splitRef(positional[0])
	paramMap, err := pairs(params, "param")
	if err != nil {
		return err
	}
	req := &v1.InspectRequest{SourceId: *source, Repo: repo, Revision: revision, Groups: groups, RuntimeIds: runtimes, Params: paramMap, SlotId: *slot}
	for _, c := range contexts {
		n, err := strconv.ParseUint(c, 10, 32)
		if err != nil {
			return fmt.Errorf("ctx %q: %w", c, err)
		}
		req.Contexts = append(req.Contexts, uint32(n))
	}
	resp, err := e.cl.estimate.Inspect(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return e.print(resp.Msg, func(w io.Writer) {
		m := resp.Msg.GetModel()
		fmt.Fprintf(w, "%s@%s %s\n", m.GetRepo(), m.GetRevision(), m.GetCommit())
		section(w, "weight groups")
		var rows [][]string
		for _, d := range resp.Msg.GetDescriptors() {
			p := d.GetParams()
			rows = append(rows, []string{d.GetGroup(), d.GetFormatId(), d.GetArchitecture(), humanCount(d.GetParameterCount()), fmt.Sprintf("%.2f", d.GetBitsPerWeight()), estimate.Human(d.GetTotalBytes()), num(p["n_layer"]), num(p["n_ctx_train"]), num(p["n_expert"])})
		}
		table(w, []string{"GROUP", "FORMAT", "ARCH", "PARAMS", "BPW", "WEIGHTS", "LAYERS", "CTX TRAIN", "EXPERTS"}, rows)
		section(w, "fit")
		rows = nil
		for _, r := range resp.Msg.GetRows() {
			plan := r.GetPlan()
			rows = append(rows, []string{r.GetGroup(), r.GetRuntimeId(), fitContext(r), loud(plan.GetVerdict()), loud(r.GetFree().GetVerdict()), poolUsage(plan, v1.PoolKind_POOL_KIND_DEVICE), poolUsage(plan, v1.PoolKind_POOL_KIND_HOST), estimate.Human(plan.GetCacheBytes()), placements(plan), plan.GetDetail()})
		}
		table(w, []string{"GROUP", "RUNTIME", "CTX", "TOTAL", "FREE NOW", "DEVICE", "HOST", "CACHE", "PLACEMENT", "DETAIL"}, rows)
		for _, warn := range resp.Msg.GetWarnings() {
			fmt.Fprintln(w, "warning:", warn)
		}
	})
}

// The context a fit row was planned at, the solved one saying so with the length it settled on
func fitContext(r *v1.FitRow) string {
	if r.GetContext() != 0 {
		return strconv.FormatUint(uint64(r.GetContext()), 10)
	}
	return "auto " + r.GetPlan().GetParams()["n_ctx"]
}

func num(f float64) string {
	if f == 0 {
		return "-"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
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
		if p.GetPoolId() == text.Enum(v1.PoolKind_POOL_KIND_HOST) {
			t.host += p.GetCount()
		} else {
			t.device += p.GetCount()
		}
	}
	var parts []string
	for _, k := range order {
		t := counts[k]
		if t.device == 0 {
			parts = append(parts, text.Enum(k)+":host")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d/%d", text.Enum(k), t.device, t.device+t.host))
	}
	for _, p := range plan.GetSkipped() {
		parts = append(parts, text.Enum(p.GetKind())+":disk")
	}
	return strings.Join(parts, " ")
}

// One line summing up a plan
func planLine(plan *v1.MemoryPlan) string {
	return fmt.Sprintf("%s device %s host %s cache %s %s %s", loud(plan.GetVerdict()), poolUsage(plan, v1.PoolKind_POOL_KIND_DEVICE), poolUsage(plan, v1.PoolKind_POOL_KIND_HOST), estimate.Human(plan.GetCacheBytes()), placements(plan), plan.GetDetail())
}
