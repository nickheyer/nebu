package runtimes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// One adapter an endpoint serves
type Adapter struct {
	// Name requests put in the model field
	Name string
	// File or directory the adapter was loaded from
	Path string
}

// Loads and unloads adapters on one running endpoint
type AdapterClient interface {
	// Adapters the endpoint applies to requests now
	List(ctx context.Context) ([]Adapter, error)
	Load(ctx context.Context, name, path string) error
	Unload(ctx context.Context, name string) error
}

// Client every adapter and sleep call goes through, each bounded by the caller's context
var controlHTTP = &http.Client{Timeout: 5 * time.Minute}

// Sends one request and decodes its JSON answer into out, a status outside 2xx failing with the body
func callJSON(ctx context.Context, method, url string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := controlHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	return nil
}

// Adapter API of vLLM and SGLang, named loads and unloads beside a models listing that names each adapter's parent
type namedAdapters struct {
	base, load, unload string
}

// Adapters the models listing names with a parent model
func (a namedAdapters) List(ctx context.Context) ([]Adapter, error) {
	var listing struct {
		Data []struct {
			ID     string  `json:"id"`
			Root   string  `json:"root"`
			Parent *string `json:"parent"`
		} `json:"data"`
	}
	if err := callJSON(ctx, http.MethodGet, a.base+"/v1/models", nil, &listing); err != nil {
		return nil, err
	}
	var out []Adapter
	for _, m := range listing.Data {
		if m.Parent != nil && *m.Parent != "" {
			out = append(out, Adapter{Name: m.ID, Path: m.Root})
		}
	}
	return out, nil
}

func (a namedAdapters) Load(ctx context.Context, name, path string) error {
	return callJSON(ctx, http.MethodPost, a.base+a.load, map[string]string{"lora_name": name, "lora_path": path}, nil)
}

func (a namedAdapters) Unload(ctx context.Context, name string) error {
	return callJSON(ctx, http.MethodPost, a.base+a.unload, map[string]string{"lora_name": name}, nil)
}

// Adapter API of llama.cpp, which switches adapters loaded at launch on and off by scale
type llamaAdapters struct {
	base string
}

// One adapter llama-server loaded at launch, with its id and current scale
type llamaAdapter struct {
	ID    int     `json:"id"`
	Path  string  `json:"path"`
	Scale float64 `json:"scale"`
}

// Name a llama.cpp adapter answers to, its file name without the extension
func llamaAdapterName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

func (a llamaAdapters) all(ctx context.Context) ([]llamaAdapter, error) {
	var list []llamaAdapter
	if err := callJSON(ctx, http.MethodGet, a.base+"/lora-adapters", nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// Adapters applied at a nonzero scale
func (a llamaAdapters) List(ctx context.Context) ([]Adapter, error) {
	list, err := a.all(ctx)
	if err != nil {
		return nil, err
	}
	var out []Adapter
	for _, l := range list {
		if l.Scale != 0 {
			out = append(out, Adapter{Name: llamaAdapterName(l.Path), Path: l.Path})
		}
	}
	return out, nil
}

// Applies the launch-time adapter matching the path, or the name when the path is empty, at scale 1
func (a llamaAdapters) Load(ctx context.Context, name, path string) error {
	return a.scale(ctx, name, path, 1)
}

func (a llamaAdapters) Unload(ctx context.Context, name string) error {
	return a.scale(ctx, name, "", 0)
}

// Posts every adapter's scale with the matching one set, since llama-server disables any adapter the list leaves out
func (a llamaAdapters) scale(ctx context.Context, name, path string, scale float64) error {
	list, err := a.all(ctx)
	if err != nil {
		return err
	}
	type entry struct {
		ID    int     `json:"id"`
		Scale float64 `json:"scale"`
	}
	body := make([]entry, 0, len(list))
	found := false
	for _, l := range list {
		e := entry{ID: l.ID, Scale: l.Scale}
		if path != "" && filepath.Clean(l.Path) == filepath.Clean(path) || path == "" && llamaAdapterName(l.Path) == name {
			e.Scale, found = scale, true
		}
		body = append(body, e)
	}
	if !found {
		return fmt.Errorf("%w: llama-server switches only adapters it loaded at launch, and none of its %d is %s", ErrParam, len(list), strings.TrimSpace(name+" "+path))
	}
	return callJSON(ctx, http.MethodPost, a.base+"/lora-adapters", body, nil)
}
