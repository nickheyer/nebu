package stub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type modelCard struct {
	ID          string  `json:"id"`
	Object      string  `json:"object"`
	Created     int64   `json:"created"`
	OwnedBy     string  `json:"owned_by"`
	Root        string  `json:"root"`
	Parent      *string `json:"parent"`
	MaxModelLen int     `json:"max_model_len,omitempty"`
}

type modelList struct {
	Object string      `json:"object"`
	Data   []modelCard `json:"data"`
}

// Lists the base model and every resident adapter with the base model as parent, owned by sglang in SGLang mode
func (s *Stub) handleModels(w http.ResponseWriter, r *http.Request) {
	if s.eng.loading(time.Now()) {
		writeError(w, errLoading())
		return
	}
	owner := "nebu-stub"
	if s.cfg.Names == NamesSGLang {
		owner = "sglang"
	}
	list := modelList{Object: "list", Data: []modelCard{{
		ID: s.cfg.Model, Object: "model", Created: s.created, OwnedBy: owner, Root: s.cfg.Model,
		MaxModelLen: s.cfg.NumGPUBlocks * s.cfg.BlockSize,
	}}}
	for _, a := range s.eng.residentAdapters() {
		parent := s.cfg.Model
		list.Data = append(list.Data, modelCard{ID: a.name, Object: "model", Created: a.created, OwnedBy: owner, Root: a.path, Parent: &parent})
	}
	writeJSON(w, http.StatusOK, list)
}

// Answers SGLang server info, resolved launch fields beside scheduler handshake fields and one scheduler's state
func (s *Stub) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	if s.eng.loading(time.Now()) {
		writeError(w, errLoading())
		return
	}
	started := float64(s.created)
	writeJSON(w, http.StatusOK, map[string]any{
		"model_path":           s.cfg.Model,
		"served_model_name":    s.cfg.Model,
		"page_size":            s.cfg.BlockSize,
		"max_running_requests": s.cfg.MaxNumSeqs,
		"enable_lora":          true,
		"max_loras_per_batch":  s.cfg.MaxLoras,
		"status":               "ready",
		"max_total_num_tokens": s.cfg.NumGPUBlocks * s.cfg.BlockSize,
		"kv_events":            nil,
		"startup_time":         started,
		"internal_states":      []map[string]any{{"world_size": 1, "startup_time": started, "effective_max_running_requests_per_dp": s.cfg.MaxNumSeqs}},
		"version":              "0.0.0.dev0",
		"frontend":             "python",
	})
}

// Lists every server slot as llama.cpp does, answering 503 under fail_on_no_slot when no slot is idle
func (s *Stub) handleSlots(w http.ResponseWriter, r *http.Request) {
	if s.eng.loading(time.Now()) {
		writeError(w, errLoading())
		return
	}
	slots, idle := s.eng.slotList()
	if r.URL.Query().Get("fail_on_no_slot") != "" && idle == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"code": http.StatusServiceUnavailable, "message": "no slot available", "type": "unavailable_error"}})
		return
	}
	writeJSON(w, http.StatusOK, slots)
}

// Lists resident adapters as llama.cpp lists its launch adapters, each applied at scale 1
func (s *Stub) handleLoraAdapters(w http.ResponseWriter, r *http.Request) {
	if s.eng.loading(time.Now()) {
		writeError(w, errLoading())
		return
	}
	out := []map[string]any{}
	for i, a := range s.eng.residentAdapters() {
		out = append(out, map[string]any{"id": i, "path": a.path, "scale": json.Number("1.0"), "task_name": "", "prompt_prefix": ""})
	}
	writeJSON(w, http.StatusOK, out)
}

// Loads an adapter after the load delay, or answers 202 when it joins the waiting list
func (s *Stub) handleLoad(w http.ResponseWriter, r *http.Request) {
	if s.eng.loading(time.Now()) {
		writeError(w, errLoading())
		return
	}
	var req struct {
		LoraName string `json:"lora_name"`
		LoraPath string `json:"lora_path"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.LoraName == "" || req.LoraPath == "" {
		writeError(w, badRequest("lora_name", "Both 'lora_name' and 'lora_path' must be provided."))
		return
	}
	l, queued, err := s.eng.loadAdapter(req.LoraName, req.LoraPath, time.Now())
	if err != nil {
		writeError(w, err)
		return
	}
	if queued {
		writeText(w, http.StatusAccepted, fmt.Sprintf("Queued: LoRA adapter '%s' waits for a free slot.", req.LoraName))
		return
	}
	select {
	case <-l.loaded:
	case <-r.Context().Done():
		return
	}
	if l.fail != nil {
		writeError(w, l.fail)
		return
	}
	writeText(w, http.StatusOK, fmt.Sprintf("Success: LoRA adapter '%s' added successfully.", req.LoraName))
}

// Unloads a resident, loading or waiting adapter
func (s *Stub) handleUnload(w http.ResponseWriter, r *http.Request) {
	if s.eng.loading(time.Now()) {
		writeError(w, errLoading())
		return
	}
	var req struct {
		LoraName string `json:"lora_name"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.LoraName == "" {
		writeError(w, badRequest("lora_name", "'lora_name' must be provided."))
		return
	}
	if err := s.eng.unloadAdapter(req.LoraName, time.Now()); err != nil {
		writeError(w, err)
		return
	}
	writeText(w, http.StatusOK, fmt.Sprintf("Success: LoRA adapter '%s' removed successfully.", req.LoraName))
}

// Answers 200 while serving and 503 naming the state while loading or paused
func (s *Stub) handleHealth(w http.ResponseWriter, r *http.Request) {
	if st := s.eng.status(time.Now()); st != "" {
		writeText(w, http.StatusServiceUnavailable, st)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Reports the faults in force
func (s *Stub) handleFaults(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.eng.faultState())
}

// Applies a fault update and reports the faults in force afterward
func (s *Stub) handleControl(w http.ResponseWriter, r *http.Request) {
	var c Control
	if !decode(w, r, &c) {
		return
	}
	st, err := s.Control(c)
	if err != nil {
		writeError(w, badRequest("", "%v", err))
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// Reports counters and kept per request records as JSON
func (s *Stub) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Stats())
}
