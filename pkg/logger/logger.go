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

// How many of its own lines the daemon keeps for the Host page
const recentLines = 5000

// Builds a logger from config, writing to file when set, and the ring of the last lines it wrote, which
// the daemon streams to the Host page
func New(cfg *v1.Logging) (*slog.Logger, *launch.Log, io.Closer, error) {
	var w io.Writer = os.Stderr
	var closer io.Closer = io.NopCloser(nil)
	if cfg.GetFile() != "" {
		f, err := os.OpenFile(cfg.GetFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, nil, nil, err
		}
		w, closer = f, f
	}
	recent := launch.NewLog(recentLines)
	w = io.MultiWriter(w, &lineWriter{ring: recent})
	opts := &slog.HandlerOptions{Level: level(cfg.GetLevel())}
	var h slog.Handler
	if strings.EqualFold(cfg.GetFormat(), "json") {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h), recent, closer, nil
}

// Feeds whole lines of handler output into the ring, holding a partial line until its end arrives
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

func level(s string) slog.Level {
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
