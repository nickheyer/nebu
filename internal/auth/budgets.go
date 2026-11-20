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

// What a credential's provider reports of its limits and how long its refusals keep it out, kept in memory
type standing struct {
	reported map[string]*reported
	// When the cooldown after a refusal ends, and the wait the last refusal naming none was given
	until   time.Time
	backoff time.Duration
}

// A window with the models it counts, every model when nil
type window struct {
	*v1.CredentialWindow
	models []string
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

// Refuses budgets with no unit or period, a period missing what it reads, an unknown zone or a name used twice
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
		if b.GetUnit() == v1.BudgetUnit_BUDGET_UNIT_PERCENT_LEFT {
			if b.GetWindow() == "" {
				return bad("reads percent left of a reported window, name it")
			}
			continue
		}
		if _, err := time.LoadLocation(b.GetZone()); err != nil {
			return bad("names zone %q, which is unknown", b.GetZone())
		}
		switch b.GetPeriod() {
		case v1.BudgetPeriod_BUDGET_PERIOD_ROLLING:
			if b.GetSeconds() == 0 {
				return bad("rolls over no seconds")
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
	c.prune(ctx)
	for {
		select {
		case <-ctx.Done():
			c.flush(context.WithoutCancel(ctx))
			return
		case <-flush.C:
			c.flush(ctx)
		case <-prune.C:
			c.prune(ctx)
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

// Notes what a provider answered under a credential: the limits its headers report, a refusal's cooldown, and a success clearing it
func (c *Credentials) Answered(cred *v1.ProviderCredential, status int, header http.Header, retry time.Duration) {
	_, cat, err := c.provider(cred.GetSourceId())
	if err != nil || cat == nil {
		return
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.standings[cred.GetId()]
	if st == nil {
		st = &standing{reported: map[string]*reported{}}
		c.standings[cred.GetId()] = st
	}
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
		if retry <= 0 {
			if st.backoff == 0 || now.After(st.until.Add(cooldownMost)) {
				st.backoff = cooldownFirst
			} else {
				st.backoff = min(st.backoff*2, cooldownMost)
			}
			retry = st.backoff
		}
		st.until = now.Add(retry)
		c.log.Info("provider refused a credential as over its limits, cooling down", "source", cred.GetSourceId(), "credential", cred.GetName(), "until", st.until)
	case status < http.StatusBadRequest:
		st.until, st.backoff = time.Time{}, 0
	}
}

// Forgets what was kept in memory of a removed credential
func (c *Credentials) forgetStanding(id string) {
	c.mu.Lock()
	delete(c.standings, id)
	c.mu.Unlock()
}

// The credential's windows as they stand: each budget, each limit its provider reported, and a cooldown running
func (c *Credentials) windows(ctx context.Context, row db.CredentialRow, now time.Time) []window {
	st := standing{reported: map[string]*reported{}}
	c.mu.Lock()
	if s := c.standings[row.Credential.GetId()]; s != nil {
		st.until = s.until
		for name, r := range s.reported {
			copied := *r
			st.reported[name] = &copied
		}
	}
	c.mu.Unlock()
	var out []window
	for _, b := range row.Credential.GetBudgets() {
		out = append(out, window{CredentialWindow: c.budget(ctx, row, b, st, now), models: b.GetModels()})
	}
	for _, name := range slices.Sorted(maps.Keys(st.reported)) {
		out = append(out, window{CredentialWindow: st.reported[name].window(name, now)})
	}
	if now.Before(st.until) {
		out = append(out, window{CredentialWindow: &v1.CredentialWindow{Name: cooldownName, Kind: v1.WindowKind_WINDOW_KIND_COOLDOWN, Limit: 1, Used: 1, ResetsAt: timestamppb.New(st.until), Spent: true}})
	}
	return out
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
	if b.GetUnit() == v1.BudgetUnit_BUDGET_UNIT_PERCENT_LEFT {
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
			reset = time.Unix((oldest+1)*db.UsageBucket, 0).Add(time.Duration(b.GetSeconds()) * time.Second)
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

// When a budget's period began and when it next resets, a rolling period's reset left to the usage it holds
func period(b *v1.CredentialBudget, st standing, now time.Time) (time.Time, time.Time) {
	loc, err := time.LoadLocation(b.GetZone())
	if err != nil {
		loc = time.UTC
	}
	t := now.In(loc)
	switch b.GetPeriod() {
	case v1.BudgetPeriod_BUDGET_PERIOD_ROLLING:
		return now.Add(-time.Duration(b.GetSeconds()) * time.Second), time.Time{}
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
// window counting the model. With none it answers false and the earliest a window holding one back resets.
func (c *Credentials) chosen(ctx context.Context, rows []db.CredentialRow, model string) (db.CredentialRow, time.Time, bool) {
	now := c.now()
	var reset time.Time
	for _, r := range rows {
		held := false
		for _, w := range c.windows(ctx, r, now) {
			if !w.holds(model) {
				continue
			}
			held = true
			if at := w.GetResetsAt(); at != nil && (reset.IsZero() || at.AsTime().Before(reset)) {
				reset = at.AsTime()
			}
		}
		if !held {
			return r, time.Time{}, true
		}
	}
	return db.CredentialRow{}, reset, false
}

// The refusal for a request whose every credential is spent, waiting until the first resets
func spent(name, model string, reset, now time.Time) error {
	what := cmp.Or(model, "its models")
	if reset.IsZero() {
		return &requestcontrol.SpentError{Message: fmt.Sprintf("every credential for %s is spent for %s", name, what)}
	}
	wait := reset.Sub(now)
	return &requestcontrol.SpentError{Message: fmt.Sprintf("every credential for %s is spent for %s, the first resets in %s", name, what, wait.Round(time.Second)), Retry: wait}
}
