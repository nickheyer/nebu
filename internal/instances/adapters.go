package instances

import (
	"context"
	"fmt"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/text"
)

// The adapter client of a ready instance, refusing one whose launch switches no adapters
func (m *Manager) adapterClient(id string) (*instance, runtimes.AdapterClient, error) {
	in, err := m.find(id)
	if err != nil {
		return nil, nil, err
	}
	rec := in.snapshot()
	if rec.GetState() != v1.InstanceState_INSTANCE_STATE_READY {
		return nil, nil, fmt.Errorf("%w: %s is %s, adapters change on a ready instance", runtimes.ErrParam, rec.GetName(), text.Enum(rec.GetState()))
	}
	client := adaptersOf(in.rt, rec)
	if client == nil {
		return nil, nil, fmt.Errorf("%w: %s launched %s without adapter support, relaunch it with adapters on", runtimes.ErrParam, rec.GetRuntimeId(), rec.GetName())
	}
	return in, client, nil
}

// Loads an adapter into a ready instance and republishes it with an endpoint scoped to the adapter
func (m *Manager) LoadAdapter(ctx context.Context, id, name, path string) error {
	in, client, err := m.adapterClient(id)
	if err != nil {
		return err
	}
	if err := client.Load(ctx, name, path); err != nil {
		return fmt.Errorf("load adapter %s: %w", name, err)
	}
	return m.publish(ctx, in)
}

// Unloads an adapter from a ready instance and republishes it without the adapter's endpoint
func (m *Manager) UnloadAdapter(ctx context.Context, id, name string) error {
	in, client, err := m.adapterClient(id)
	if err != nil {
		return err
	}
	if err := client.Unload(ctx, name); err != nil {
		return fmt.Errorf("unload adapter %s: %w", name, err)
	}
	return m.publish(ctx, in)
}

// Lists the adapters a ready instance applies now
func (m *Manager) ListAdapters(ctx context.Context, id string) ([]runtimes.Adapter, error) {
	_, client, err := m.adapterClient(id)
	if err != nil {
		return nil, err
	}
	return client.List(ctx)
}
