package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/requestcontrol"
)

// Credentials on a clock the test moves
func clocked(t *testing.T, start time.Time) (*Credentials, *time.Time) {
	t.Helper()
	c := openCredentials(t)
	now := start
	c.now = func() time.Time { return now }
	return c, &now
}

func keep(t *testing.T, c *Credentials, source, name string, budgets ...*v1.CredentialBudget) *v1.ProviderCredential {
	t.Helper()
	cred, err := c.Put(context.Background(), "ann", &v1.PutCredentialRequest{SourceId: source, Name: name, Secret: "sk-" + name, Budgets: budgets, BudgetsSet: len(budgets) > 0})
	if err != nil {
		t.Fatal(err)
	}
	return cred
}

// The named window of a credential as the owner's listing shows it
func windowOf(t *testing.T, c *Credentials, id, name string) *v1.CredentialWindow {
	t.Helper()
	list, err := c.List(context.Background(), "ann")
	if err != nil {
		t.Fatal(err)
	}
	for _, cred := range list {
		if cred.GetId() != id {
			continue
		}
		for _, w := range cred.GetWindows() {
			if w.GetName() == name {
				return w
			}
		}
	}
	return nil
}

func usageOf(t *testing.T, c *Credentials, id string) []*v1.CredentialUsage {
	t.Helper()
	got, err := c.Usage(context.Background(), "ann", id, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Usage adds up per model, is written when the loop stops, is read by a fresh start, and goes with its credential
func TestLedgerAddsUpAndSurvivesARestart(t *testing.T) {
	c, _ := clocked(t, time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	cred := keep(t, c, "lab", "main")
	c.Record(cred, "m", 10, 5, 2)
	c.Record(cred, "m", 20, 1, 0)
	c.Record(cred, "other", 1, 1, 1)
	if got := usageOf(t, c, cred.GetId()); len(got) != 2 || got[0].GetModel() != "m" || got[0].GetRequests() != 2 || got[0].GetInputTokens() != 30 || got[0].GetOutputTokens() != 6 || got[0].GetCachedTokens() != 2 {
		t.Fatalf("usage %v", got)
	}
	c.Record(cred, "m", 100, 0, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	cancel()
	<-done
	again := NewCredentials(c.db, c.sources, c.log, c.host)
	again.now = c.now
	if got := usageOf(t, again, cred.GetId()); len(got) != 2 || got[0].GetRequests() != 3 || got[0].GetInputTokens() != 130 {
		t.Fatalf("after a restart %v", got)
	}
	if _, err := again.Usage(context.Background(), "bob", cred.GetId(), time.Time{}, time.Time{}); !errors.Is(err, ErrUnknownCredential) {
		t.Fatalf("another owner reads it, got %v", err)
	}
	if err := again.Delete(context.Background(), "ann", cred.GetId()); err != nil {
		t.Fatal(err)
	}
	again.Record(cred, "m", 1, 1, 0)
	again.flush(context.Background())
	if len(again.unwritten) != 0 {
		t.Fatal("usage of a removed credential stays waiting")
	}
	if rows, err := again.db.CredentialUsage(context.Background(), "ann", "", 0, 1<<40); err != nil || len(rows) != 0 {
		t.Fatalf("a removed credential's usage stays, %v %v", rows, err)
	}
}

// A day counted in a named zone rolls over at its start there, and usage past the keep is pruned
func TestLedgerRollsOverAndPrunes(t *testing.T) {
	// 23:30 in New York, 04:30 UTC
	c, now := clocked(t, time.Date(2026, 11, 20, 4, 30, 0, 0, time.UTC))
	day := &v1.CredentialBudget{Name: "daily", Unit: v1.BudgetUnit_BUDGET_UNIT_TOKENS, Limit: 1000, Period: v1.BudgetPeriod_BUDGET_PERIOD_DAY, Zone: "America/New_York"}
	week := &v1.CredentialBudget{Name: "weekly", Unit: v1.BudgetUnit_BUDGET_UNIT_REQUESTS, Limit: 10, Period: v1.BudgetPeriod_BUDGET_PERIOD_WEEK, Start: 5, Zone: "America/New_York"}
	month := &v1.CredentialBudget{Name: "monthly", Unit: v1.BudgetUnit_BUDGET_UNIT_OUTPUT_TOKENS, Limit: 10, Period: v1.BudgetPeriod_BUDGET_PERIOD_MONTH, Start: 20, Zone: "Asia/Tokyo"}
	cred := keep(t, c, "lab", "main", day, week, month)
	c.Record(cred, "m", 100, 50, 0)
	if w := windowOf(t, c, cred.GetId(), "daily"); w.GetUsed() != 150 || w.GetResetsAt().AsTime() != time.Date(2026, 11, 20, 5, 0, 0, 0, time.UTC) {
		t.Fatalf("before midnight in New York %v", w)
	}
	*now = time.Date(2026, 11, 20, 5, 10, 0, 0, time.UTC)
	if w := windowOf(t, c, cred.GetId(), "daily"); w.GetUsed() != 0 || w.GetResetsAt().AsTime() != time.Date(2026, 11, 21, 5, 0, 0, 0, time.UTC) {
		t.Fatalf("after midnight in New York %v", w)
	}
	// Friday the 20th began the week in New York, so the Thursday request is gone from it
	if w := windowOf(t, c, cred.GetId(), "weekly"); w.GetUsed() != 0 || w.GetResetsAt().AsTime() != time.Date(2026, 11, 27, 5, 0, 0, 0, time.UTC) {
		t.Fatalf("week %v", w)
	}
	// The 20th began at 15:00 UTC on the 19th in Tokyo, so the request counts there
	if w := windowOf(t, c, cred.GetId(), "monthly"); w.GetUsed() != 50 || !w.GetSpent() || w.GetResetsAt().AsTime() != time.Date(2026, 12, 19, 15, 0, 0, 0, time.UTC) {
		t.Fatalf("month %v", w)
	}
	*now = now.Add(-101 * 24 * time.Hour)
	c.Record(cred, "old", 1, 1, 0)
	c.flush(context.Background())
	*now = time.Date(2026, 11, 20, 5, 10, 0, 0, time.UTC)
	c.prune(context.Background())
	rows, err := c.db.CredentialUsage(context.Background(), "ann", cred.GetId(), 0, 1<<40)
	if err != nil || len(rows) != 1 || rows[0].Usage.GetModel() != "m" {
		t.Fatalf("after pruning %v %v", rows, err)
	}
}

// A budget turns spent at its limit less its reserve and back once its usage rolls out
func TestBudgetWindowTurnsSpentAndBack(t *testing.T) {
	start := time.Date(2026, 11, 20, 12, 1, 0, 0, time.UTC)
	c, now := clocked(t, start)
	hourly := &v1.CredentialBudget{Name: "hourly", Unit: v1.BudgetUnit_BUDGET_UNIT_INPUT_TOKENS, Limit: 100, Period: v1.BudgetPeriod_BUDGET_PERIOD_ROLLING, Seconds: 3600, ReservePercent: 20}
	cred := keep(t, c, "lab", "main", hourly)
	c.Record(cred, "m", 70, 500, 30)
	if w := windowOf(t, c, cred.GetId(), "hourly"); w.GetUsed() != 40 || w.GetSpent() {
		t.Fatalf("cached input counts for nothing unless asked, got %v", w)
	}
	c.Record(cred, "m", 40, 0, 0)
	w := windowOf(t, c, cred.GetId(), "hourly")
	if w.GetUsed() != 80 || !w.GetSpent() || w.GetResetsAt().AsTime() != time.Date(2026, 11, 20, 13, 5, 0, 0, time.UTC) {
		t.Fatalf("spent at the limit less the reserve, got %v", w)
	}
	*now = time.Date(2026, 11, 20, 13, 6, 0, 0, time.UTC)
	if w := windowOf(t, c, cred.GetId(), "hourly"); w.GetUsed() != 0 || w.GetSpent() {
		t.Fatalf("rolled out, got %v", w)
	}
}

// A reported window follows the provider's limit headers, spent with nothing left until its reset in each format in use
func TestReportedWindowFollowsResets(t *testing.T) {
	start := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	c, now := clocked(t, start)
	claude := keep(t, c, "anthropic", "claude")
	gpt := keep(t, c, "openai", "gpt")
	c.Answered(claude, http.StatusOK, http.Header{
		"Anthropic-Ratelimit-Tokens-Limit":       {"1000"},
		"Anthropic-Ratelimit-Tokens-Remaining":   {"0"},
		"Anthropic-Ratelimit-Tokens-Reset":       {start.Add(time.Minute).Format(time.RFC3339)},
		"Anthropic-Ratelimit-Requests-Limit":     {"50"},
		"Anthropic-Ratelimit-Requests-Remaining": {"49"},
		"Anthropic-Ratelimit-Requests-Reset":     {start.Add(time.Second).Format(time.RFC3339)},
	}, 0)
	c.Answered(gpt, http.StatusOK, http.Header{"X-Ratelimit-Limit-Tokens": {"150000"}, "X-Ratelimit-Remaining-Tokens": {"0"}, "X-Ratelimit-Reset-Tokens": {"6m0s"}}, 0)
	if w := windowOf(t, c, claude.GetId(), "tokens"); !w.GetSpent() || w.GetUsed() != 1000 || w.GetKind() != v1.WindowKind_WINDOW_KIND_REPORTED || w.GetResetsAt().AsTime() != start.Add(time.Minute) {
		t.Fatalf("tokens %v", w)
	}
	if w := windowOf(t, c, claude.GetId(), "requests"); w.GetSpent() || w.GetUsed() != 1 {
		t.Fatalf("requests %v", w)
	}
	if w := windowOf(t, c, gpt.GetId(), "tokens"); !w.GetSpent() || w.GetResetsAt().AsTime() != start.Add(6*time.Minute) {
		t.Fatalf("a duration reset %v", w)
	}
	if _, _, err := c.Headers(context.Background(), "ann", "anthropic", "claude-x", nil); !errors.As(err, new(*requestcontrol.SpentError)) {
		t.Fatalf("a spent reported window holds the credential back, got %v", err)
	}
	*now = start.Add(2 * time.Minute)
	if w := windowOf(t, c, claude.GetId(), "tokens"); w.GetSpent() || w.GetUsed() != 0 {
		t.Fatalf("replenished after its reset, got %v", w)
	}
	if cred, _, err := c.Headers(context.Background(), "ann", "anthropic", "claude-x", nil); err != nil || cred.GetId() != claude.GetId() {
		t.Fatalf("back after its reset, got %v %v", cred, err)
	}
}

// A refusal naming no wait cools down from thirty seconds doubling to ten minutes, a success clears it, and a named wait is kept
func TestCooldownBacksOffAndClears(t *testing.T) {
	start := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	c, now := clocked(t, start)
	cred := keep(t, c, "lab", "main")
	for _, want := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute} {
		c.Answered(cred, http.StatusTooManyRequests, http.Header{}, 0)
		w := windowOf(t, c, cred.GetId(), cooldownName)
		if !w.GetSpent() || w.GetKind() != v1.WindowKind_WINDOW_KIND_COOLDOWN || w.GetResetsAt().AsTime() != now.Add(want) {
			t.Fatalf("cooldown %v, want %s", w, want)
		}
		*now = w.GetResetsAt().AsTime()
		if windowOf(t, c, cred.GetId(), cooldownName) != nil {
			t.Fatal("the cooldown outlives its end")
		}
	}
	c.Answered(cred, http.StatusOK, http.Header{}, 0)
	c.Answered(cred, http.StatusTooManyRequests, http.Header{}, 0)
	if w := windowOf(t, c, cred.GetId(), cooldownName); w.GetResetsAt().AsTime() != now.Add(30*time.Second) {
		t.Fatalf("a success starts the backoff over, got %v", w)
	}
	c.Answered(cred, http.StatusOK, http.Header{}, 0)
	if windowOf(t, c, cred.GetId(), cooldownName) != nil {
		t.Fatal("a success leaves the cooldown")
	}
	c.Answered(cred, http.StatusTooManyRequests, http.Header{}, 5*time.Second)
	if w := windowOf(t, c, cred.GetId(), cooldownName); w.GetResetsAt().AsTime() != now.Add(5*time.Second) {
		t.Fatalf("the wait the provider named, got %v", w)
	}
}

// chosen passes over a credential with a spent window counting the model and, with every one spent, names the earliest reset
func TestChosenSkipsSpent(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	c, now := clocked(t, start)
	gpt4 := &v1.CredentialBudget{Name: "gpt4", Models: []string{"gpt-4*", "o*-mini"}, Unit: v1.BudgetUnit_BUDGET_UNIT_REQUESTS, Limit: 10, ReservePercent: 50, Period: v1.BudgetPeriod_BUDGET_PERIOD_DAY}
	first := keep(t, c, "lab", "first", gpt4)
	second := keep(t, c, "lab", "second")
	pick := func(model string) string {
		t.Helper()
		cred, _, err := c.Headers(ctx, "ann", "lab", model, nil)
		if err != nil {
			t.Fatal(err)
		}
		return cred.GetName()
	}
	for range 4 {
		c.Record(first, "gpt-4o", 1, 1, 0)
	}
	if got := pick("gpt-4o"); got != "first" {
		t.Fatalf("four of ten with half held back is not spent, got %s", got)
	}
	c.Record(first, "o3-mini", 1, 1, 0)
	for model, want := range map[string]string{"gpt-4o": "second", "o3-mini": "second", "gpt-3.5": "first", "o3": "first"} {
		if got := pick(model); got != want {
			t.Fatalf("%s went under %s, want %s", model, got, want)
		}
	}
	c.Answered(second, http.StatusTooManyRequests, http.Header{}, 10*time.Second)
	_, _, err := c.Headers(ctx, "ann", "lab", "gpt-4o", nil)
	se, ok := errors.AsType[*requestcontrol.SpentError](err)
	if !ok || se.Retry != 10*time.Second {
		t.Fatalf("every credential spent, got %v", err)
	}
	c.Answered(first, http.StatusTooManyRequests, http.Header{}, 0)
	*now = start.Add(11 * time.Second)
	if got := pick("gpt-3.5"); got != "second" {
		t.Fatalf("the first cools down and the second came back, got %s", got)
	}
	c.Answered(second, http.StatusTooManyRequests, http.Header{}, time.Minute)
	_, _, err = c.Headers(ctx, "ann", "lab", "gpt-4o", nil)
	if se, ok := errors.AsType[*requestcontrol.SpentError](err); !ok || se.Retry != 19*time.Second {
		t.Fatalf("the first's cooldown is the earliest left, got %v", err)
	}
}

// Budgets missing what they need are refused, and a position moves a credential among its source's
func TestBudgetsCheckedAndPlacement(t *testing.T) {
	ctx := context.Background()
	c := openCredentials(t)
	for _, b := range []*v1.CredentialBudget{
		{Name: "no unit", Limit: 1, Period: v1.BudgetPeriod_BUDGET_PERIOD_DAY},
		{Name: "no period", Unit: v1.BudgetUnit_BUDGET_UNIT_TOKENS, Limit: 1},
		{Name: "no seconds", Unit: v1.BudgetUnit_BUDGET_UNIT_TOKENS, Limit: 1, Period: v1.BudgetPeriod_BUDGET_PERIOD_ROLLING},
		{Name: "zone", Unit: v1.BudgetUnit_BUDGET_UNIT_TOKENS, Limit: 1, Period: v1.BudgetPeriod_BUDGET_PERIOD_DAY, Zone: "Mars/Olympus"},
		{Name: "percent", Unit: v1.BudgetUnit_BUDGET_UNIT_PERCENT_LEFT, Limit: 80},
		{Name: "month", Unit: v1.BudgetUnit_BUDGET_UNIT_TOKENS, Limit: 1, Period: v1.BudgetPeriod_BUDGET_PERIOD_MONTH, Start: 30},
	} {
		if _, err := c.Put(ctx, "ann", &v1.PutCredentialRequest{SourceId: "lab", Secret: "k", Budgets: []*v1.CredentialBudget{b}, BudgetsSet: true}); !errors.Is(err, ErrProviderCredential) {
			t.Fatalf("%s: %v", b.GetName(), err)
		}
	}
	a, b, d := keep(t, c, "lab", "a"), keep(t, c, "lab", "b"), keep(t, c, "lab", "d")
	daily := &v1.CredentialBudget{Name: "daily", Unit: v1.BudgetUnit_BUDGET_UNIT_TOKENS, Limit: 5, Period: v1.BudgetPeriod_BUDGET_PERIOD_DAY}
	if _, err := c.Put(ctx, "ann", &v1.PutCredentialRequest{Id: d.GetId(), Position: 1, Budgets: []*v1.CredentialBudget{daily}, BudgetsSet: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Put(ctx, "ann", &v1.PutCredentialRequest{Id: d.GetId(), Name: "renamed"}); err != nil {
		t.Fatal(err)
	}
	rows, err := c.db.SourceCredentials(ctx, "ann", "lab")
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, r := range rows {
		order = append(order, r.Credential.GetId())
	}
	if len(order) != 3 || order[0] != d.GetId() || order[1] != a.GetId() || order[2] != b.GetId() || rows[0].Credential.GetPosition() != 1 || rows[2].Credential.GetPosition() != 3 {
		t.Fatalf("order %v", rows)
	}
	if got := rows[0].Credential.GetBudgets(); len(got) != 1 || got[0].GetName() != "daily" {
		t.Fatalf("an update naming no budgets keeps them, got %v", got)
	}
}
