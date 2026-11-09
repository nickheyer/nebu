package config

import (
	"context"
	"os"
	"time"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// The config file Load reads for path, empty when none sits on the search path
func File(path string) (string, error) { return locate(path) }

// What a poll saw of the config file, a change in any of it meaning the file was rewritten
type mark struct {
	file string
	mod  time.Time
	size int64
}

// The current mark of the config file, zero when there is none
func markOf(path string) mark {
	file, err := locate(path)
	if err != nil || file == "" {
		return mark{}
	}
	info, err := os.Stat(file)
	if err != nil {
		return mark{}
	}
	return mark{file: file, mod: info.ModTime(), size: info.Size()}
}

// Reloads from path on every file change seen each interval and on every kick, handing each outcome to apply until ctx ends
func Watch(ctx context.Context, path string, interval time.Duration, kick <-chan struct{}, apply func(cfg *v1.Config, err error)) {
	last := markOf(path)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-kick:
			last = markOf(path)
		case <-tick.C:
			now := markOf(path)
			if now == last {
				continue
			}
			last = now
		}
		cfg, err := Load(path)
		apply(cfg, err)
	}
}
