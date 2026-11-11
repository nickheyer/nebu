// Package logger builds structured loggers from config.
package logger

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/nickheyer/nebu/pkg/launch"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Host page log capacity.
const recentLines = 5000

// Builds a logger with optional file output, a ring buffer for the Host page, and the level handle a reload sets
func New(cfg *v1.Logging) (*slog.Logger, *launch.Log, *slog.LevelVar, io.Closer, error) {
	var w io.Writer = os.Stderr
	var closer io.Closer = io.NopCloser(nil)
	if cfg.GetFile() != "" {
		f, err := os.OpenFile(cfg.GetFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		w, closer = f, f
	}
	recent := launch.NewLog(recentLines)
	w = io.MultiWriter(w, &lineWriter{ring: recent})
	level := new(slog.LevelVar)
	level.Set(ParseLevel(cfg.GetLevel()))
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.EqualFold(cfg.GetFormat(), "json") {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h), recent, level, closer, nil
}

// Buffers partial writes and adds complete lines to the ring.
type lineWriter struct {
	ring *launch.Log
	mu   sync.Mutex
	rest []byte
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rest = append(l.rest, p...)
	for {
		i := bytes.IndexByte(l.rest, '\n')
		if i < 0 {
			break
		}
		l.ring.Write(string(l.rest[:i]))
		l.rest = l.rest[i+1:]
	}
	if len(l.rest) == 0 {
		l.rest = nil
	}
	return len(p), nil
}

// The level a config names, info for anything unknown
// The name a level is configured by, debug, info, warn or error
func LevelName(l slog.Level) string {
	switch {
	case l <= slog.LevelDebug:
		return "debug"
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	}
	return "info"
}

func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}
