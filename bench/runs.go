package bench

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/proto/nebu/v1/nebuv1connect"
	"google.golang.org/protobuf/proto"
)

// Pause between two reads of a pool while waiting on it
const pollEvery = 100 * time.Millisecond

// Condition types a pool reports, as internal/pools names them
const (
	conditionAccepted     = "Accepted"
	conditionResolvedRefs = "ResolvedRefs"
	conditionReady        = "Ready"
)

// How RunConfigs switches a pool between scheduler configurations
type Plan struct {
	// Pool whose scheduler_config each run sets
	Pool string
	// Scheduler configuration names, one run each in order
	Configs []string
	// Longest wait for the pool to apply a configuration with every endpoint ready, default one minute
	ReadyTimeout time.Duration
	// Runs before each configuration is applied, such as restarting stubs, nil for nothing
	Before func(ctx context.Context, config string) error
}

// Fills the zero timeout and refuses a plan with no pool, no configuration, or a repeated or empty name
func (p Plan) withDefaults() (Plan, error) {
	if p.ReadyTimeout == 0 {
		p.ReadyTimeout = time.Minute
	}
	switch {
	case p.Pool == "":
		return p, errors.New("switching scheduler configurations needs a pool")
	case len(p.Configs) == 0:
		return p, errors.New("the plan names no scheduler configuration")
	case p.ReadyTimeout < 0:
		return p, fmt.Errorf("ready timeout %v is negative", p.ReadyTimeout)
	}
	for i, c := range p.Configs {
		if c == "" {
			return p, fmt.Errorf("scheduler configuration %d is empty", i)
		}
		if slices.Contains(p.Configs[:i], c) {
			return p, fmt.Errorf("scheduler configuration %s is named twice", c)
		}
	}
	return p, nil
}

// Replays requests once per configuration after setting it on the pool, then puts the pool's spec back
func RunConfigs(ctx context.Context, api nebuv1connect.GatewayServiceClient, gatewayURL, token string, reqs []Request, cfg Config, plan Plan) (reports []*Report, err error) {
	plan, err = plan.withDefaults()
	if err != nil {
		return nil, err
	}
	if err := knownConfigs(ctx, api, plan.Configs); err != nil {
		return nil, err
	}
	got, err := api.GetPool(ctx, connect.NewRequest(&v1.GetPoolRequest{Id: plan.Pool}))
	if err != nil {
		return nil, fmt.Errorf("reading pool %s: %w", plan.Pool, err)
	}
	original := got.Msg.GetPool().GetSpec()
	defer func() {
		restore, cancel := context.WithTimeout(context.WithoutCancel(ctx), plan.ReadyTimeout)
		defer cancel()
		if _, rerr := api.PutPool(restore, connect.NewRequest(&v1.PutPoolRequest{Id: plan.Pool, Spec: original})); rerr != nil {
			err = errors.Join(err, fmt.Errorf("restoring pool %s: %w", plan.Pool, rerr))
		}
	}()
	for _, name := range plan.Configs {
		if plan.Before != nil {
			if err := plan.Before(ctx, name); err != nil {
				return reports, fmt.Errorf("preparing %s: %w", name, err)
			}
		}
		if err := SetSchedulerConfig(ctx, api, plan.Pool, name, plan.ReadyTimeout); err != nil {
			return reports, err
		}
		run := cfg
		run.Name = name
		rep, err := ReplayRequests(ctx, gatewayURL, token, reqs, run)
		if err != nil {
			return reports, fmt.Errorf("replaying under %s: %w", name, err)
		}
		reports = append(reports, rep)
	}
	return reports, nil
}

// Refuses names the gateway holds no scheduler configuration for, listing the ones it holds
func knownConfigs(ctx context.Context, api nebuv1connect.GatewayServiceClient, names []string) error {
	resp, err := api.ListSchedulerConfigs(ctx, connect.NewRequest(&v1.ListSchedulerConfigsRequest{}))
	if err != nil {
		return fmt.Errorf("listing scheduler configurations: %w", err)
	}
	var held []string
	for _, c := range resp.Msg.GetConfigs() {
		held = append(held, c.GetName())
	}
	for _, n := range names {
		if !slices.Contains(held, n) {
			slices.Sort(held)
			return fmt.Errorf("the gateway holds no scheduler configuration %s, it holds %s", n, strings.Join(held, ", "))
		}
	}
	return nil
}

// Sets the pool's scheduler configuration and waits until the pool runs it with every endpoint ready
func SetSchedulerConfig(ctx context.Context, api nebuv1connect.GatewayServiceClient, pool, name string, timeout time.Duration) error {
	got, err := api.GetPool(ctx, connect.NewRequest(&v1.GetPoolRequest{Id: pool}))
	if err != nil {
		return fmt.Errorf("reading pool %s: %w", pool, err)
	}
	spec := proto.Clone(got.Msg.GetPool().GetSpec()).(*v1.PoolSpec)
	spec.SchedulerConfig = name
	if _, err := api.PutPool(ctx, connect.NewRequest(&v1.PutPoolRequest{Id: pool, Spec: spec})); err != nil {
		return fmt.Errorf("setting scheduler configuration %s on pool %s: %w", name, pool, err)
	}
	return WaitPool(ctx, api, pool, timeout, func(p *v1.Pool) (bool, error) {
		if p.GetSpec().GetSchedulerConfig() != name {
			return false, nil
		}
		return allReady(p)
	})
}

// Whether the pool's current generation is accepted, resolved and ready on every endpoint, an error when refused
func allReady(p *v1.Pool) (bool, error) {
	gen := p.GetMeta().GetGeneration()
	conds := slices.Clone(p.GetStatus().GetConditions())
	for _, parent := range p.GetStatus().GetParents() {
		conds = append(conds, parent.GetConditions()...)
	}
	ready := false
	for _, c := range conds {
		if c.GetObservedGeneration() != gen {
			return false, nil
		}
		isTrue := c.GetStatus() == v1.ConditionStatus_CONDITION_STATUS_TRUE
		switch c.GetType() {
		case conditionAccepted, conditionResolvedRefs:
			if c.GetStatus() == v1.ConditionStatus_CONDITION_STATUS_FALSE {
				return false, fmt.Errorf("pool %s is not %s, %s", p.GetId(), strings.ToLower(c.GetType()), c.GetMessage())
			}
			if !isTrue {
				return false, nil
			}
		case conditionReady:
			if !isTrue {
				return false, nil
			}
			ready = true
		}
	}
	st := p.GetStatus()
	if !ready || st.GetTotalEndpoints() == 0 || st.GetReadyEndpoints() != st.GetTotalEndpoints() {
		return false, nil
	}
	for _, ep := range st.GetEndpoints() {
		if !ep.GetReady() || !ep.GetHealthy() {
			return false, nil
		}
	}
	return true, nil
}

// Reads the pool until done answers true, failing on its error, a read error or the timeout
func WaitPool(ctx context.Context, api nebuv1connect.GatewayServiceClient, pool string, timeout time.Duration, done func(*v1.Pool) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	var last *v1.Pool
	for {
		got, err := api.GetPool(ctx, connect.NewRequest(&v1.GetPoolRequest{Id: pool}))
		if err == nil {
			last = got.Msg.GetPool()
			ok, derr := done(last)
			if derr != nil {
				return derr
			}
			if ok {
				return nil
			}
		} else if ctx.Err() == nil {
			return fmt.Errorf("reading pool %s: %w", pool, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("pool %s did not settle within %v, last seen %s: %w", pool, timeout, describePool(last), context.Cause(ctx))
		case <-tick.C:
		}
	}
}

// The configuration, readiness and conditions of a pool in one line
func describePool(p *v1.Pool) string {
	if p == nil {
		return "nothing"
	}
	parts := []string{fmt.Sprintf("config %s, %d of %d endpoints ready", p.GetSpec().GetSchedulerConfig(), p.GetStatus().GetReadyEndpoints(), p.GetStatus().GetTotalEndpoints())}
	for _, c := range p.GetStatus().GetConditions() {
		parts = append(parts, fmt.Sprintf("%s %s %s", c.GetType(), strings.TrimPrefix(c.GetStatus().String(), "CONDITION_STATUS_"), c.GetMessage()))
	}
	return strings.Join(parts, ", ")
}
