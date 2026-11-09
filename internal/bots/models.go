package bots

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Longest wait for the gateway's model listing
const modelsTimeout = 10 * time.Second

// A ready route and what the gateway reports it serves, completion, images or videos
type readyModel struct {
	name string
	caps []string
}

// Whether the route serves chat, images or video
func (m readyModel) serves(kind string) bool {
	want := map[string]string{"chat": "completion", "image": "images", "video": "videos"}[kind]
	return slices.Contains(m.caps, want)
}

// Ready routes from the pools view, each with the capabilities the gateway's model listing gives it
func (r *runner) readyModels() ([]readyModel, error) {
	ctx, cancel := context.WithTimeout(r.ctx, modelsTimeout)
	defer cancel()
	resp, err := r.get(ctx, "/v1/models")
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, attachmentMax))
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("listing models answered %s", resp.Status)
	}
	var list struct {
		Data []struct {
			ID           string   `json:"id"`
			Capabilities []string `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}
	caps := map[string][]string{}
	for _, d := range list.Data {
		caps[d.ID] = d.Capabilities
	}
	var out []readyModel
	for _, rt := range r.m.Pools.Routes() {
		if rt.GetStatus().GetState() == v1.RouteState_ROUTE_STATE_READY {
			out = append(out, readyModel{name: rt.GetName(), caps: caps[rt.GetName()]})
		}
	}
	return out, nil
}
