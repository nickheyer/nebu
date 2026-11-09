package stub

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"
)

// Partial update of injected faults, absent fields left unchanged
type Control struct {
	// pause or resume
	Action string `json:"action,omitempty"`
	// Share of inference requests answered 500, between 0 and 1
	FailRate *float64 `json:"fail_rate,omitempty"`
	// Queue depth above which inference requests are answered 429, negative turning it off
	RejectAboveQueue *int `json:"reject_above_queue,omitempty"`
	// Upcoming inference requests whose connection is closed without a response
	DropConnections *int `json:"drop_connections,omitempty"`
	// Factor applied to every emulated duration scheduled from now on
	LatencyMultiplier *float64 `json:"latency_multiplier,omitempty"`
}

// Injected failure settings currently in force
type FaultState struct {
	Paused            bool    `json:"paused"`
	FailRate          float64 `json:"fail_rate"`
	RejectAboveQueue  int     `json:"reject_above_queue"`
	DropConnections   int     `json:"drop_connections"`
	LatencyMultiplier float64 `json:"latency_multiplier"`
}

// Injected failure settings with the accumulator spacing failures evenly
type faults struct {
	failRate    float64
	failAcc     float64
	rejectAbove int
	drop        int
	latency     float64
}

type verdict int

const (
	verdictPass verdict = iota
	verdictDrop
	verdictLoading
	verdictFail
)

// Decides whether an inference request is dropped, refused while loading, failed, or let through
func (e *engine) gate(now time.Time) verdict {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.faults.drop > 0 {
		e.faults.drop--
		return verdictDrop
	}
	if now.Before(e.ready) {
		return verdictLoading
	}
	if e.faults.failRate > 0 {
		e.faults.failAcc += e.faults.failRate
		if e.faults.failAcc >= 1-1e-9 {
			e.faults.failAcc--
			return verdictFail
		}
	}
	return verdictPass
}

// Validates every field of an update before applying any of them
func (e *engine) control(c Control) (FaultState, error) {
	if c.Action != "" && c.Action != "pause" && c.Action != "resume" {
		return FaultState{}, fmt.Errorf("unknown action %q, want pause or resume", c.Action)
	}
	if c.FailRate != nil && !(*c.FailRate >= 0 && *c.FailRate <= 1) {
		return FaultState{}, errors.New("fail_rate must be between 0 and 1")
	}
	if c.DropConnections != nil && *c.DropConnections < 0 {
		return FaultState{}, errors.New("drop_connections must not be negative")
	}
	if c.LatencyMultiplier != nil && (!(*c.LatencyMultiplier > 0) || math.IsInf(*c.LatencyMultiplier, 0)) {
		return FaultState{}, errors.New("latency_multiplier must be a finite positive number")
	}
	e.mu.Lock()
	switch c.Action {
	case "pause":
		e.paused = true
	case "resume":
		e.paused = false
	}
	if c.FailRate != nil {
		e.faults.failRate, e.faults.failAcc = *c.FailRate, 0
	}
	if c.RejectAboveQueue != nil {
		e.faults.rejectAbove = *c.RejectAboveQueue
	}
	if c.DropConnections != nil {
		e.faults.drop = *c.DropConnections
	}
	if c.LatencyMultiplier != nil {
		e.faults.latency = *c.LatencyMultiplier
	}
	st := e.faultStateLocked()
	e.mu.Unlock()
	e.kick()
	return st, nil
}

// Injected settings in force at this moment
func (e *engine) faultState() FaultState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.faultStateLocked()
}

// Injected settings in force, read with the lock held
func (e *engine) faultStateLocked() FaultState {
	return FaultState{
		Paused:            e.paused,
		FailRate:          e.faults.failRate,
		RejectAboveQueue:  e.faults.rejectAbove,
		DropConnections:   e.faults.drop,
		LatencyMultiplier: e.faults.latency,
	}
}

// Applies drop, loading and failure faults to an inference request, reporting whether it may proceed
func (s *Stub) gate(w http.ResponseWriter) bool {
	switch s.eng.gate(time.Now()) {
	case verdictDrop:
		dropConnection(w)
		return false
	case verdictLoading:
		writeError(w, errLoading())
		return false
	case verdictFail:
		writeError(w, errInjected())
		return false
	}
	return true
}

// Closes the client socket without writing a response
func dropConnection(w http.ResponseWriter) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	conn.Close()
}
