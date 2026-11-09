package daemon

import (
	"github.com/nickheyer/nebu/internal/gateway"
	"github.com/nickheyer/nebu/pkg/store"
)

// The metadata key a PEFT adapter config names its base model under, as the peft format flattens it
const adapterBaseKey = "adapter.base_model_name_or_path"

// The model store's PEFT adapters as the gateway loads them, named by repo with their base model and weight directory
type storeAdapters struct {
	store *store.Store
}

func (s storeAdapters) Adapters() ([]gateway.StoredAdapter, error) {
	manifests, err := s.store.ListManifests()
	if err != nil {
		return nil, err
	}
	var out []gateway.StoredAdapter
	for _, m := range manifests {
		base := m.GetDescriptor_().GetMetadata()[adapterBaseKey]
		if m.GetFormatId() != "peft" || base == "" {
			continue
		}
		out = append(out, gateway.StoredAdapter{Name: m.GetRepo(), Base: base, Path: m.GetPath()})
	}
	return out, nil
}
