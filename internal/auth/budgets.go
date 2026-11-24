package auth

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// How often recorded usage is written
	usageFlush = 5 * time.Second
	// How long usage is kept, and how often what is older is pruned
	usageKeep  = 100 * 24 * time.Hour
	usagePrune = 24 * time.Hour
	// How far back usage is read when a range names no start
	usageSince = 30 * 24 * time.Hour
	// The wait after a refusal that names none, doubling while refusals continue up to the most
	cooldownFirst = 30 * time.Second
	cooldownMost  = 10 * time.Minute
	// The name the cooldown window goes by
	cooldownName = "cooldown"
	// The name the window goes by while the provider rejected the credential itself
	rejectedName = "rejected"
	// Models one credential keeps a standing for, the one answered longest ago forgotten past them
	maxStandings = 64
	// Budgets one credential may carry
	maxBudgets = 32
)

// Usage waiting to be written, by credential, model and bucket
type usageKey struct {
	id, model string
	bucket    int64
}

// What a provider last reported of one of its limits
type reported struct {
	limit, remaining float64
	reset            time.Time
	// When the period it reports began, the reset before it once one was seen
	since time.Time
}

// How long refusals keep a credential out, or one model under it
type cooldown struct {
	// When the cooldown after a refusal ends, and the wait the last refusal naming none was given
	until   time.Time
	backoff time.Duration
}

// What a credential's provider reports of its limits for one model and how long its refusals keep the model out, kept in memory
type standing struct {
	reported map[string]*reported
	cooldown
	// When the provider last answered for the model
	seen time.Time
}

// What a provider's answers under one credential left in memory: a standing by model, "" for answers naming none
// and holding back every model, and the cooldown after the provider rejected the credential itself
type answers struct {
	models   map[string]*standing
	rejected cooldown
	// Whether a request went under the credential while it was rejected, whose success ends the rejection at once rather
	// than passing for the answer to a request sent before it
	retried bool
}

// A window with the models it counts, every model when nil
type window struct {
	*v1.CredentialWindow
	models []string
	// Whether it is the wait after the provider rejected the credential itself, which passes the credential over for
	// another and never keeps a request from the last one left
	rejected bool
}

// Whether the window holds back a request for the model
func (w window) holds(model string) bool { return w.GetSpent() && counts(w.models, model) }

// Whether a model id is one the patterns name, * standing for any run of characters, every model when there are none
func counts(patterns []string, model string) bool {
	if len(patterns) == 0 {
		return true
	}
	return slices.ContainsFunc(patterns, func(p string) bool { return matches(p, model) })
}

func matches(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// Refuses budgets with no unit or period, a period missing what it reads, a rolling period shorter than a usage bucket or
// longer than usage is kept, an unknown zone or a name used twice
func checkBudgets(list []*v1.CredentialBudget) error {
	if len(list) > maxBudgets {
		return fmt.Errorf("%w: a credential carries at most %d budgets", ErrProviderCredential, maxBudgets)
	}
	seen := map[string]bool{}
	for _, b := range list {
		name := b.GetName()
		bad := func(format string, args ...any) error {
			return fmt.Errorf("%w: budget %q %s", ErrProviderCredential, name, fmt.Sprintf(format, args...))
		}
		switch {
		case name == "" || len(name) > maxCredentialName:
			return fmt.Errorf("%w: every budget needs a name of at most %d characters", ErrProviderCredential, maxCredentialName)
		case seen[name]:
			return bad("is named twice")
		case b.GetUnit() == v1.BudgetUnit_BUDGET_UNIT_UNSPECIFIED:
			return bad("counts nothing, give it a unit")
		case !(b.GetLimit() > 0):
			return bad("needs a limit above zero")
		case slices.Contains(b.GetModels(), ""):
			return bad("names an empty model")
		}
		seen[name] = true
		if b.GetUnit() == v1.BudgetUnit_BUDGET_UNIT_PERCENT_USED {
			if b.GetWindow() == "" {
				return bad("reads the percent used of a reported window, name it")
			}
			continue
		}
		if _, err := time.LoadLocation(b.GetZone()); err != nil {
			return bad("names zone %q, which is unknown", b.GetZone())
		}
		switch b.GetPeriod() {
		case v1.BudgetPeriod_BUDGET_PERIOD_ROLLING:
			switch s := int64(b.GetSeconds()); {
			case s == 0:
				return bad("rolls over no seconds")
			case s < db.UsageBucket:
				return bad("rolls over %d seconds, at least %d, the span usage is counted in", s, db.UsageBucket)
			case s > int64(usageKeep/time.Second):
				return bad("rolls over %d seconds, at most %d, as long as usage is kept", s, int64(usageKeep/time.Second))
			}
		case v1.BudgetPeriod_BUDGET_PERIOD_DAY:
			if b.GetStart() > 23 {
				return bad("starts its day at hour %d", b.GetStart())
			}
		case v1.BudgetPeriod_BUDGET_PERIOD_WEEK:
			if b.GetStart() > 6 {
				return bad("starts its week on weekday %d, Sunday is 0", b.GetStart())
			}
		case v1.BudgetPeriod_BUDGET_PERIOD_MONTH:
			if b.GetStart() > 28 {
				return bad("starts its month on day %d, at most 28", b.GetStart())
			}
		case v1.BudgetPeriod_BUDGET_PERIOD_REPORTED:
			if b.GetWindow() == "" {
				return bad("follows a reported window, name it")
			}
		default:
			return bad("counts over no period")
		}
	}
	return nil
}

// Adds one finished request's tokens to what the credential was used for with the model, written with the next batch
func (c *Credentials) Record(cred *v1.ProviderCredential, model string, in, out, cached int) {
	key := usageKey{id: cred.GetId(), model: model, bucket: c.now().Unix() / db.UsageBucket}
	c.ledger.Lock()
	defer c.ledger.Unlock()
	u := c.unwritten[key]
	if u == nil {
		u = &v1.CredentialUsage{CredentialId: key.id, Model: model}
		c.unwritten[key] = u
	}
	u.Requests++
	u.InputTokens += uint64(max(in, 0))
	u.OutputTokens += uint64(max(out, 0))
	u.CachedTokens += uint64(max(cached, 0))
}

// Writes recorded usage every few seconds and once more as ctx ends, pruning usage past its keep each day
func (c *Credentials) Run(ctx context.Context) {
	flush := time.NewTicker(usageFlush)
	defer flush.Stop()
	prune := time.NewTicker(usagePrune)
	defer prune.Stop()
	revoke := time.NewTicker(revokeEvery)
	defer revoke.Stop()
	c.prune(ctx)
	c.revokeQueued(ctx)
	for {
		select {
		case <-ctx.Done():
			c.flush(context.WithoutCancel(ctx))
			return
		case <-flush.C:
			c.flush(ctx)
		case <-prune.C:
			c.prune(ctx)
		case <-revoke.C:
			c.revokeQueued(ctx)
		}
	}
}

// Writes the usage recorded since the last write, keeping it for the next when the write fails
func (c *Credentials) flush(ctx context.Context) {
	c.ledger.Lock()
	defer c.ledger.Unlock()
	if len(c.unwritten) == 0 {
		return
	}
	rows := make([]db.UsageRow, 0, len(c.unwritten))
	for k, u := range c.unwritten {
		rows = append(rows, db.UsageRow{Usage: u, Bucket: k.bucket})
	}
	if err := c.db.AddUsage(ctx, rows); err != nil {
		c.log.Warn("credential usage not written, kept for the next write", "rows", len(rows), "err", err)
		return
	}
	clear(c.unwritten)
}

func (c *Credentials) prune(ctx context.Context) {
	n, err := c.db.PruneUsage(ctx, c.now().Add(-usageKeep).Unix()/db.UsageBucket)
	if err != nil {
		c.log.Warn("credential usage past its keep not pruned", "err", err)
		return
	}
	if n > 0 {
		c.log.Debug("credential usage past its keep pruned", "rows", n)
	}
	c.pruneStandings(ctx)
}

// Forgets what providers answered under credentials removed since, the answer to a request in flight at a removal having kept it again
func (c *Credentials) pruneStandings(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.standings) == 0 {
		return
	}
	ids, err := c.db.CredentialIDs(ctx)
	if err != nil {
		return
	}
	for id := range c.standings {
		if _, kept := ids[id]; !kept {
			delete(c.standings, id)
		}
	}
}

// What the owner's credentials, or one of them, were used for between from and to by model, the most requests first
func (c *Credentials) Usage(ctx context.Context, owner, id string, from, to time.Time) ([]*v1.CredentialUsage, error) {
	if id != "" {
		if _, err := c.get(ctx, owner, id); err != nil {
			return nil, err
		}
	}
	now := c.now()
	if from.IsZero() {
		from = now.Add(-usageSince)
	}
	if to.IsZero() {
		to = now
	}
	c.flush(ctx)
	rows, err := c.db.CredentialUsage(ctx, owner, id, from.Unix()/db.UsageBucket, to.Unix()/db.UsageBucket+1)
	if err != nil {
		return nil, err
	}
	out := make([]*v1.CredentialUsage, len(rows))
	for i, r := range rows {
		out[i] = r.Usage
	}
	return out, nil
}

// One credential's usage from bucket from on, by model, written and waiting alike
func (c *Credentials) used(ctx context.Context, row db.CredentialRow, from, to int64) ([]db.UsageRow, error) {
	c.ledger.RLock()
	defer c.ledger.RUnlock()
	rows, err := c.db.CredentialUsage(ctx, row.Owner, row.Credential.GetId(), from, to)
	if err != nil {
		return nil, err
	}
	for k, u := range c.unwritten {
		if k.id != row.Credential.GetId() || k.bucket < from || k.bucket >= to {
			continue
		}
		i := slices.IndexFunc(rows, func(r db.UsageRow) bool { return r.Usage.GetModel() == k.model })
		if i < 0 {
			rows = append(rows, db.UsageRow{Usage: &v1.CredentialUsage{CredentialId: k.id, Model: k.model}, Bucket: k.bucket})
			i = len(rows) - 1
		}
		r := &rows[i]
		r.Usage.Requests += u.GetRequests()
		r.Usage.InputTokens += u.GetInputTokens()
		r.Usage.OutputTokens += u.GetOutputTokens()
		r.Usage.CachedTokens += u.GetCachedTokens()
		r.Bucket = min(r.Bucket, k.bucket)
	}
	return rows, nil
}

// Notes what a provider answered under a credential for a model: the limits its headers report and a refusal's cooldown for that model,
// a rejection of the credential itself for every model, and a success starting the backoff over once the cooldown ran out
func (c *Credentials) Answered(cred *v1.ProviderCredential, model string, status int, header http.Header, retry time.Duration) {
	_, cat, err := c.provider(cred.GetSourceId())
	if err != nil || cat == nil {
		return
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.standings[cred.GetId()]
	if a == nil {
		a = &answers{models: map[string]*standing{}}
		c.standings[cred.GetId()] = a
	}
	st := a.models[model]
	if st == nil {
		if len(a.models) >= maxStandings {
			delete(a.models, slices.MinFunc(slices.Collect(maps.Keys(a.models)), func(x, y string) int { return a.models[x].seen.Compare(a.models[y].seen) }))
		}
		st = &standing{reported: map[string]*reported{}}
		a.models[model] = st
	}
	st.seen = now
	if l := cat.Limits; l != nil {
		for _, name := range l.Windows {
			limit, remaining, reset, ok := l.Read(header, name, now)
			if !ok {
				continue
			}
			r := st.reported[name]
			switch {
			case r == nil:
				r = &reported{since: now}
				st.reported[name] = r
			case !r.reset.IsZero() && !now.Before(r.reset):
				r.since = r.reset
			}
			r.limit, r.remaining, r.reset = limit, remaining, reset
		}
	}
	switch {
	case status == http.StatusTooManyRequests:
		st.refuse(now, retry)
		c.log.Info("provider refused a credential as over its limits, cooling down", "source", cred.GetSourceId(), "credential", cred.GetName(), "model", model, "until", st.until)
	case status == http.StatusUnauthorized:
		a.retried = false
		a.rejected.refuse(now, retry)
		c.log.Warn("provider rejected a credential, passing it over", "source", cred.GetSourceId(), "credential", cred.GetName(), "until", a.rejected.until)
	case status < http.StatusBadRequest:
		st.answered(now)
		if a.retried {
			a.rejected, a.retried = cooldown{}, false
		} else {
			a.rejected.answered(now)
		}
	}
}

// Notes that a request goes under a credential while its provider's rejection of it stands
func (c *Credentials) retrying(id string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a := c.standings[id]; a != nil && now.Before(a.rejected.until) {
		a.retried = true
	}
}

// Starts the cooldown after a refusal for the wait it named, or naming none for one doubling while refusals continue
func (cd *cooldown) refuse(now time.Time, retry time.Duration) {
	if retry <= 0 {
		if cd.backoff == 0 || now.After(cd.until.Add(cooldownMost)) {
			cd.backoff = cooldownFirst
		} else {
			cd.backoff = min(cd.backoff*2, cooldownMost)
		}
		// A refusal naming no wait never ends a cooldown sooner than the wait the provider named for it
		if until := now.Add(cd.backoff); until.After(cd.until) {
			cd.until = until
		}
		return
	}
	cd.until = now.Add(retry)
}

// Starts the backoff over after a success once the cooldown ran out, a success while it runs coming from a request sent before the refusal
func (cd *cooldown) answered(now time.Time) {
	if !now.Before(cd.until) {
		*cd = cooldown{}
	}
}

// Forgets what was kept in memory of a removed credential
func (c *Credentials) forgetStanding(id string) {
	c.mu.Lock()
	delete(c.standings, id)
	c.mu.Unlock()
}

// A copy of what the provider's answers under a credential left, for the model and answers naming none, or every model for none named
func (c *Credentials) standingOf(id, model string) (map[string]standing, cooldown) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.standings[id]
	if a == nil {
		return nil, cooldown{}
	}
	out := map[string]standing{}
	for m, st := range a.models {
		if model != "" && m != "" && m != model {
			continue
		}
		kept := standing{reported: make(map[string]*reported, len(st.reported)), cooldown: st.cooldown}
		for name, r := range st.reported {
			copied := *r
			kept.reported[name] = &copied
		}
		out[m] = kept
	}
	return out, a.rejected
}

// The standing a budget reads for a model, the model's reported windows over those answers naming none left
func readFor(kept map[string]standing, model string) standing {
	st := standing{reported: map[string]*reported{}}
	maps.Copy(st.reported, kept[""].reported)
	if model != "" {
		maps.Copy(st.reported, kept[model].reported)
	}
	return st
}

// A window's name with the model it was kept for
func named(name, model string) string {
	if model == "" {
		return name
	}
	return name + " for " + model
}

// The credential's windows as they stand for a request for the model: each budget, each limit its provider reported and a cooldown running,
// those kept for one model holding back that model alone, and the wait after a rejection. A budget reading a reported window reads the model's.
// With no model named, as the owner's listing has them, every model's are read, named for it, and such a budget shows again for each that reported it.
func (c *Credentials) windows(ctx context.Context, row db.CredentialRow, now time.Time, model string) []window {
	kept, rejected := c.standingOf(row.Credential.GetId(), model)
	scopes := []string{""}
	if model != "" {
		scopes = append(scopes, model)
	} else {
		for _, m := range slices.Sorted(maps.Keys(kept)) {
			if m != "" {
				scopes = append(scopes, m)
			}
		}
	}
	var out []window
	for _, b := range row.Credential.GetBudgets() {
		out = append(out, window{CredentialWindow: c.budget(ctx, row, b, readFor(kept, model), now), models: b.GetModels()})
		reads := b.GetUnit() == v1.BudgetUnit_BUDGET_UNIT_PERCENT_USED || b.GetPeriod() == v1.BudgetPeriod_BUDGET_PERIOD_REPORTED
		if model != "" || !reads {
			continue
		}
		for _, m := range scopes[1:] {
			if kept[m].reported[b.GetWindow()] == nil || !counts(b.GetModels(), m) {
				continue
			}
			w := c.budget(ctx, row, b, readFor(kept, m), now)
			w.Name = named(w.GetName(), m)
			out = append(out, window{CredentialWindow: w, models: []string{m}})
		}
	}
	for _, m := range scopes {
		st := kept[m]
		var models []string
		if m != "" {
			models = []string{m}
		}
		for _, name := range slices.Sorted(maps.Keys(st.reported)) {
			out = append(out, window{CredentialWindow: st.reported[name].window(named(name, m), now), models: models})
		}
		if now.Before(st.until) {
			out = append(out, window{CredentialWindow: cooling(named(cooldownName, m), st.until), models: models})
		}
	}
	if now.Before(rejected.until) {
		out = append(out, window{CredentialWindow: cooling(rejectedName, rejected.until), rejected: true})
	}
	return out
}

// A cooldown running as a window, spent until it ends
func cooling(name string, until time.Time) *v1.CredentialWindow {
	return &v1.CredentialWindow{Name: name, Kind: v1.WindowKind_WINDOW_KIND_COOLDOWN, Limit: 1, Used: 1, ResetsAt: timestamppb.New(until), Spent: true}
}

// A reported limit as a window, spent while nothing is left before a known reset and replenished once it passed
func (r *reported) window(name string, now time.Time) *v1.CredentialWindow {
	w := &v1.CredentialWindow{Name: name, Kind: v1.WindowKind_WINDOW_KIND_REPORTED, Limit: r.limit, Used: max(r.limit-r.remaining, 0)}
	if r.reset.IsZero() {
		return w
	}
	w.ResetsAt = timestamppb.New(r.reset)
	if !now.Before(r.reset) {
		w.Used = 0
	}
	w.Spent = now.Before(r.reset) && r.remaining <= 0
	return w
}

// A budget as a window: what it counts used since its period began against its limit, spent once all but the reserve is used
func (c *Credentials) budget(ctx context.Context, row db.CredentialRow, b *v1.CredentialBudget, st standing, now time.Time) *v1.CredentialWindow {
	w := &v1.CredentialWindow{Name: b.GetName(), Kind: v1.WindowKind_WINDOW_KIND_BUDGET, Limit: b.GetLimit()}
	var reset time.Time
	if b.GetUnit() == v1.BudgetUnit_BUDGET_UNIT_PERCENT_USED {
		if r := st.reported[b.GetWindow()]; r != nil && r.limit > 0 && (r.reset.IsZero() || now.Before(r.reset)) {
			w.Used, reset = max(r.limit-r.remaining, 0)/r.limit*100, r.reset
		}
	} else {
		var from time.Time
		from, reset = period(b, st, now)
		rows, err := c.used(ctx, row, from.Unix()/db.UsageBucket, now.Unix()/db.UsageBucket+1)
		if err != nil {
			c.log.Warn("credential usage not read, the budget counts as unused", "credential", row.Credential.GetId(), "budget", b.GetName(), "err", err)
		}
		oldest := int64(-1)
		for _, r := range rows {
			if counts(b.GetModels(), r.Usage.GetModel()) {
				w.Used += amount(b, r.Usage)
				if oldest < 0 || r.Bucket < oldest {
					oldest = r.Bucket
				}
			}
		}
		if b.GetPeriod() == v1.BudgetPeriod_BUDGET_PERIOD_ROLLING && oldest >= 0 {
			reset = time.Unix((oldest+1)*db.UsageBucket, 0).Add(rolling(b))
		}
	}
	if !reset.IsZero() {
		w.ResetsAt = timestamppb.New(reset)
	}
	w.Spent = w.Used >= w.Limit*(1-float64(b.GetReservePercent())/100)
	return w
}

// What a budget counts of one model's usage
func amount(b *v1.CredentialBudget, u *v1.CredentialUsage) float64 {
	in := u.GetInputTokens()
	if !b.GetCached() {
		in -= min(u.GetCachedTokens(), in)
	}
	switch b.GetUnit() {
	case v1.BudgetUnit_BUDGET_UNIT_TOKENS:
		return float64(in + u.GetOutputTokens())
	case v1.BudgetUnit_BUDGET_UNIT_INPUT_TOKENS:
		return float64(in)
	case v1.BudgetUnit_BUDGET_UNIT_OUTPUT_TOKENS:
		return float64(u.GetOutputTokens())
	case v1.BudgetUnit_BUDGET_UNIT_REQUESTS:
		return float64(u.GetRequests())
	}
	return 0
}

// The span a rolling budget counts over. One stored before spans were bounded reads as the nearest span usage can measure,
// a bucket at least and as long as usage is kept at most, its stored seconds left as its owner wrote them.
func rolling(b *v1.CredentialBudget) time.Duration {
	return min(max(time.Duration(b.GetSeconds())*time.Second, db.UsageBucket*time.Second), usageKeep)
}

// When a budget's period began and when it next resets, a rolling period's reset left to the usage it holds
func period(b *v1.CredentialBudget, st standing, now time.Time) (time.Time, time.Time) {
	loc, err := time.LoadLocation(b.GetZone())
	if err != nil {
		loc = time.UTC
	}
	t := now.In(loc)
	switch b.GetPeriod() {
	case v1.BudgetPeriod_BUDGET_PERIOD_ROLLING:
		return now.Add(-rolling(b)), time.Time{}
	case v1.BudgetPeriod_BUDGET_PERIOD_DAY:
		start := time.Date(t.Year(), t.Month(), t.Day(), int(b.GetStart()), 0, 0, 0, loc)
		if start.After(t) {
			start = start.AddDate(0, 0, -1)
		}
		return start, start.AddDate(0, 0, 1)
	case v1.BudgetPeriod_BUDGET_PERIOD_WEEK:
		start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -((int(t.Weekday()) - int(b.GetStart()) + 7) % 7))
		return start, start.AddDate(0, 0, 7)
	case v1.BudgetPeriod_BUDGET_PERIOD_MONTH:
		start := time.Date(t.Year(), t.Month(), max(int(b.GetStart()), 1), 0, 0, 0, 0, loc)
		if start.After(t) {
			start = start.AddDate(0, -1, 0)
		}
		return start, start.AddDate(0, 1, 0)
	case v1.BudgetPeriod_BUDGET_PERIOD_REPORTED:
		if r := st.reported[b.GetWindow()]; r != nil {
			if r.reset.IsZero() || now.Before(r.reset) {
				return r.since, r.reset
			}
			return r.reset, time.Time{}
		}
	}
	return now, time.Time{}
}

// The credential a request for the model goes under among the owner's for a source: the first by position with no spent
// window counting the model. One its provider rejected is passed over for the next, but with every other spent the first
// held by its rejection alone is gone under all the same: a rejection may pass, and with nothing else to go under, waiting
// it out would turn one refusal into an outage. With none it answers false and the earliest any comes free, each once the
// last window holding it resets, one held by a window naming no reset left out.
func (c *Credentials) chosen(ctx context.Context, rows []db.CredentialRow, model string) (db.CredentialRow, time.Time, bool) {
	now := c.now()
	var reset time.Time
	var last *db.CredentialRow
	for _, r := range rows {
		held, known, rejected := false, true, true
		// When the credential comes free, the latest reset among the windows holding it
		var free time.Time
		for _, w := range c.windows(ctx, r, now, model) {
			if !w.holds(model) {
				continue
			}
			held, rejected = true, rejected && w.rejected
			if at := w.GetResetsAt(); at == nil {
				known = false
			} else if at.AsTime().After(free) {
				free = at.AsTime()
			}
		}
		if !held {
			return r, time.Time{}, true
		}
		if rejected && last == nil {
			last = &r
		}
		if known && (reset.IsZero() || free.Before(reset)) {
			reset = free
		}
	}
	if last != nil {
		return *last, time.Time{}, true
	}
	return db.CredentialRow{}, reset, false
}

// The refusal for a request whose every credential is spent, waiting until the first resets, saying so when the provider
// rejected some or all of them rather than their limits being reached
func spent(name, model string, reset, now time.Time, rejected, all int) error {
	what := fmt.Sprintf("every credential for %s is spent for %s", name, cmp.Or(model, "its models"))
	switch {
	case rejected > 0 && rejected == all:
		what = fmt.Sprintf("%s rejected every credential kept for it, check them", name)
	case rejected > 0:
		what += " or was rejected by it"
	}
	if reset.IsZero() {
		return &requestcontrol.SpentError{Message: what}
	}
	wait := reset.Sub(now)
	if rejected > 0 && rejected == all {
		return &requestcontrol.SpentError{Message: fmt.Sprintf("%s, the first is tried again in %s", what, wait.Round(time.Second)), Retry: wait}
	}
	return &requestcontrol.SpentError{Message: fmt.Sprintf("%s, the first resets in %s", what, wait.Round(time.Second)), Retry: wait}
}

// How many of the credentials their provider rejected and is not asked under again yet
func (c *Credentials) rejected(rows []db.CredentialRow, now time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range rows {
		if a := c.standings[r.Credential.GetId()]; a != nil && now.Before(a.rejected.until) {
			n++
		}
	}
	return n
}
