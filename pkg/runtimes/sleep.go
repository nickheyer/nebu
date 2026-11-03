package runtimes

import (
	"context"
	"net/http"
)

// Releases and restores an endpoint's device memory while its process lives
type SleepClient interface {
	// Offloads the weights to host memory and drops the KV cache
	Sleep(ctx context.Context) error
	// Restores the weights and the KV cache so requests run again
	Wake(ctx context.Context) error
}

// Sleep endpoints vLLM serves in dev mode when launched with sleep mode on
type vllmSleeper struct {
	base string
}

func (s vllmSleeper) Sleep(ctx context.Context) error {
	return callJSON(ctx, http.MethodPost, s.base+"/sleep?level=1", nil, nil)
}

func (s vllmSleeper) Wake(ctx context.Context) error {
	return callJSON(ctx, http.MethodPost, s.base+"/wake_up", nil, nil)
}
