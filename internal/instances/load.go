package instances

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// A scraped endpoint of the instance gave no sample within the staleness
var ErrUnanswered = errors.New("runtime did not answer its metrics")

// Samples an instance's scraped endpoints for when they last showed work, ErrUnanswered when one gave no fresh sample
func ScrapedLoad(store datalayer.Store, sampler datalayer.Sampler, staleness time.Duration) func(ctx context.Context, instanceID string) (time.Time, error) {
	return func(ctx context.Context, instanceID string) (time.Time, error) {
		var ids []datalayer.EndpointID
		for _, ep := range store.Endpoints("", func(ep datalayer.Endpoint) bool { return ep.Labels()[datalayer.LabelInstance] == instanceID }) {
			if p, ok := store.Pool(ep.Pool()); ok && p.Metrics.Scraped() {
				ids = append(ids, ep.ID())
			}
		}
		wait, cancel := context.WithTimeout(ctx, staleness)
		sampler.Sample(wait, ids)
		cancel()
		var active time.Time
		now := time.Now()
		for _, id := range ids {
			ep, ok := store.Endpoint(id)
			if !ok {
				continue
			}
			m, ok := datalayer.Attribute[*datalayer.ServerMetrics](ep, datalayer.CollectorMetrics)
			if !ok || m.Stale(now, staleness) {
				return time.Time{}, fmt.Errorf("%w: endpoint %s", ErrUnanswered, id)
			}
			if m.ActiveAt.After(active) {
				active = m.ActiveAt
			}
		}
		return active, nil
	}
}
