package instances

import (
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// Sums the running and queued requests fresh scrapes report over an instance's endpoints, false when none is fresh
func ScrapedLoad(store datalayer.Store, staleness time.Duration) func(instanceID string) (running, queued int, scraped bool) {
	return func(instanceID string) (running, queued int, scraped bool) {
		now := time.Now()
		seen := map[datalayer.Address]bool{}
		for _, ep := range store.Endpoints("", func(ep datalayer.Endpoint) bool { return ep.Labels()[datalayer.LabelInstance] == instanceID }) {
			m, ok := datalayer.Attribute[*datalayer.ServerMetrics](ep, datalayer.CollectorMetrics)
			if !ok || m.Stale(now, staleness) || seen[ep.Address()] {
				continue
			}
			seen[ep.Address()] = true
			running, queued, scraped = running+m.RunningRequests, queued+m.QueuedRequests, true
		}
		return running, queued, scraped
	}
}
