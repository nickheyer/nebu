package daemon

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/nickheyer/nebu/pkg/config"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Swaps the gateway keys and default policy to what a loaded config names, and every setting the web UI leaves unset to the config's
func (d *Daemon) Reload(cfg *v1.Config) {
	base, err := settingsBase(cfg)
	if err == nil {
		err = d.Settings.Rebase(base)
	}
	if err != nil {
		d.Log.Error("config reload refused, the running settings stay", "err", err)
		return
	}
	gw := cfg.GetGateway()
	d.Gateway.Reload(gw.GetApiKeys(), gw.GetPolicy())
	d.Log.Info("config reloaded", "keys", len(gw.GetApiKeys()), "level", d.LogLevel.Level())
}

// Reloads the config file on SIGHUP, on a kick, and whenever the file changes, until ctx ends
func (d *Daemon) watchConfig(ctx context.Context) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	kick := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
			case <-d.reloads:
			}
			select {
			case kick <- struct{}{}:
			default:
			}
		}
	}()
	config.Watch(ctx, d.ConfigPath, d.reloadEvery, kick, func(cfg *v1.Config, err error) {
		if err != nil {
			d.Log.Error("config reload refused, the running settings stay", "err", err)
			return
		}
		d.Reload(cfg)
	})
}
