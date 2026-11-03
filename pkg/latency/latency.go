// Package latency predicts time to first token and time per output token for each endpoint
package latency

import (
	"time"

	"github.com/nickheyer/nebu/pkg/datalayer"
)

// The observed inputs of one request at dispatch
type Features struct {
	PromptTokens       int
	CachedTokens       int
	QueuedRequests     int
	RunningRequests    int
	KVCacheUtilization float64
	// Requests this gateway has assumed on the endpoint beyond the scraped counts
	AssumedRequests int
}

// A prediction and its spread
type Prediction struct {
	Mean   time.Duration
	StdDev time.Duration
	// Samples the model has learned from
	Samples int
	// Whether the model learned MinSamples requests and knows the mean at these features within the noise, false when the prior still decides
	Confident bool
}

// Learns from completed requests and predicts for the next one
type Predictor interface {
	PredictTTFT(endpoint datalayer.EndpointID, f Features) Prediction
	PredictTPOT(endpoint datalayer.EndpointID, f Features) Prediction
	// Learns one completed request's latencies
	Observe(endpoint datalayer.EndpointID, f Features, ttft, tpot time.Duration)
	Forget(endpoint datalayer.EndpointID)
}
