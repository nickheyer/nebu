package gateway

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/chat"
	"github.com/nickheyer/nebu/pkg/bodyparse"
	"github.com/nickheyer/nebu/pkg/datalayer"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
)

// Answers liveness with the count of ready routes
func (g *Gateway) healthz(w http.ResponseWriter, _ *http.Request) {
	ready := 0
	for _, rt := range g.pools.Routes() {
		if rt.GetStatus().GetState() == v1.RouteState_ROUTE_STATE_READY {
			ready++
		}
	}
	chat.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "models": ready})
}

// The pool a route's rules resolved to first, false when it resolved to none the store holds
func (g *Gateway) routePool(rt *v1.Route) (datalayer.Pool, bool) {
	for _, id := range rt.GetStatus().GetPoolIds() {
		if p, ok := g.store.Pool(datalayer.PoolID(id)); ok {
			return p, true
		}
	}
	return datalayer.Pool{}, false
}

// What a route serves, images and video for media pools by their instances' modes, a pooled output alone for those launches, chat otherwise
func (g *Gateway) capabilities(rt *v1.Route) []string {
	pool, ok := g.routePool(rt)
	if !ok || g.runtimes.API(pool.Runtime) != v1.ApiFlavor_API_FLAVOR_SDCPP {
		if only := poolingOnly(rt.GetStatus().GetInventory()); only != "" {
			return []string{only}
		}
		return []string{"completion", "tools"}
	}
	out := []string{}
	for _, ep := range g.store.Endpoints(pool.ID, nil) {
		for _, mode := range g.instances.Modes(ep.Labels()[datalayer.LabelInstance]) {
			c := map[string]string{"img_gen": "images", "vid_gen": "videos"}[mode]
			if c != "" && !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	slices.Sort(out)
	return out
}

// The pooled output a launch generating no text reports alone, empty for every other launch
func poolingOnly(inv *v1.RuntimeInventory) string {
	if mods := inv.GetModalities(); len(mods) == 1 && slices.Contains(runtimes.PoolingModalities, mods[0]) {
		return mods[0]
	}
	return ""
}

// The base model a route's pool serves, empty when no pool resolved
func (g *Gateway) family(rt *v1.Route) string {
	pool, _ := g.routePool(rt)
	return pool.Model
}

// Context length, quantization, accelerator, modalities and throughput class of the launch serving a route
func inventoryEntry(inv *v1.RuntimeInventory) map[string]any {
	return map[string]any{
		"context_length":   inv.GetContextLength(),
		"quantization":     inv.GetQuantization(),
		"accelerator":      inv.GetAccelerator(),
		"modalities":       append([]string{}, inv.GetModalities()...),
		"throughput_class": inv.GetThroughputClass(),
	}
}

// The model an Ollama show request names under its older name field
func showName(doc bodyparse.Document) string {
	v, ok := doc.Field("name")
	if !ok || len(v) == 0 || v[0] != '"' {
		return ""
	}
	var name string
	if json.Unmarshal(v, &name) != nil {
		return ""
	}
	return name
}

// A route as a model in the client's protocol
func (g *Gateway) modelEntry(api v1.ApiFlavor, rt *v1.Route) map[string]any {
	created, updated := rt.GetMeta().GetCreatedAt().AsTime(), rt.GetMeta().GetUpdatedAt().AsTime()
	inv := rt.GetStatus().GetInventory()
	switch api {
	case v1.ApiFlavor_API_FLAVOR_OLLAMA:
		return map[string]any{
			"name":        rt.GetName(),
			"model":       rt.GetName(),
			"modified_at": updated.UTC().Format(time.RFC3339Nano),
			"size":        0,
			"digest":      "",
			"details":     map[string]any{"family": g.family(rt), "format": "", "parameter_size": "", "quantization_level": inv.GetQuantization()},
			"inventory":   inventoryEntry(inv),
		}
	case v1.ApiFlavor_API_FLAVOR_ANTHROPIC:
		return map[string]any{"id": rt.GetName(), "type": "model", "display_name": rt.GetName(), "created_at": created.UTC().Format(time.RFC3339), "inventory": inventoryEntry(inv)}
	}
	return map[string]any{
		"id":           rt.GetName(),
		"object":       "model",
		"created":      created.Unix(),
		"owned_by":     "nebu",
		"ready":        rt.GetStatus().GetState() == v1.RouteState_ROUTE_STATE_READY,
		"capabilities": g.capabilities(rt),
		"inventory":    inventoryEntry(inv),
	}
}

// Lists routes as models in the client's protocol, draining ones hidden and the Ollama ps list showing ready ones
func (g *Gateway) models(w http.ResponseWriter, r *http.Request) {
	api := chat.Detect(r)
	var listed []*v1.Route
	data := []map[string]any{}
	for _, rt := range g.pools.Routes() {
		state := rt.GetStatus().GetState()
		if state == v1.RouteState_ROUTE_STATE_DRAINING || r.URL.Path == chat.OllamaPs && state != v1.RouteState_ROUTE_STATE_READY {
			continue
		}
		listed = append(listed, rt)
		data = append(data, g.modelEntry(api, rt))
	}
	switch api {
	case v1.ApiFlavor_API_FLAVOR_OLLAMA:
		chat.WriteJSON(w, http.StatusOK, map[string]any{"models": data})
	case v1.ApiFlavor_API_FLAVOR_ANTHROPIC:
		resp := map[string]any{"data": data, "has_more": false}
		if len(listed) > 0 {
			resp["first_id"], resp["last_id"] = listed[0].GetName(), listed[len(listed)-1].GetName()
		}
		chat.WriteJSON(w, http.StatusOK, resp)
	default:
		chat.WriteJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
	}
}

// Answers one model in the client's protocol
func (g *Gateway) model(w http.ResponseWriter, r *http.Request) {
	api := chat.Detect(r)
	name, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, modelsPath+"/"))
	rt, ok := g.pools.Route(name)
	if !ok || rt.GetStatus().GetState() == v1.RouteState_ROUTE_STATE_DRAINING {
		chat.ClientOf(api).Error(w, chat.Failure{Status: http.StatusNotFound, Code: chat.CodeModelNotFound, Message: "model " + name + " not found"})
		return
	}
	chat.WriteJSON(w, http.StatusOK, g.modelEntry(api, rt))
}

// Answers Ollama's show request from the route and the pool behind it
func (g *Gateway) show(w http.ResponseWriter, r *http.Request) {
	client := chat.ClientOf(v1.ApiFlavor_API_FLAVOR_OLLAMA)
	body, err := readBody(w, r)
	if err != nil {
		client.Error(w, failureOf(err))
		return
	}
	name := ""
	if doc, err := parseDoc(r.URL.Path, body); err == nil {
		if name, _ = doc.Model(); name == "" {
			name = showName(doc)
		}
	}
	rt, ok := g.pools.Route(name)
	if !ok {
		client.Error(w, chat.Failure{Status: http.StatusNotFound, Code: chat.CodeModelNotFound, Message: "model " + name + " not found"})
		return
	}
	pool, _ := g.routePool(rt)
	inv := rt.GetStatus().GetInventory()
	chat.WriteJSON(w, http.StatusOK, map[string]any{
		"modelfile":    "",
		"parameters":   "",
		"template":     "",
		"details":      map[string]any{"family": pool.Model, "format": "", "parameter_size": "", "quantization_level": inv.GetQuantization()},
		"model_info":   map[string]any{"nebu.pool": string(pool.ID), "nebu.runtime": pool.Runtime, "nebu.owner": rt.GetOwnerKind() + "/" + rt.GetOwnerId(), "nebu.context_length": inv.GetContextLength(), "nebu.accelerator": inv.GetAccelerator(), "nebu.throughput_class": inv.GetThroughputClass(), "nebu.modalities": append([]string{}, inv.GetModalities()...)},
		"capabilities": g.capabilities(rt),
	})
}

// Answers the version the Ollama API reports
func (g *Gateway) version(w http.ResponseWriter, _ *http.Request) {
	chat.WriteJSON(w, http.StatusOK, map[string]any{"version": g.cfg.Version})
}
