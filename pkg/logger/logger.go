// Package logger builds structured loggers from config.
package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
)

// Builds a logger from config, writing to file when set
func New(cfg *v1.Logging) (*slog.Logger, io.Closer, error) {
	var w io.Writer = os.Stderr
	var closer io.Closer = io.NopCloser(nil)
	if cfg.GetFile() != "" {
		f, err := os.OpenFile(cfg.GetFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, nil, err
		}
		w, closer = f, f
	}
	opts := &slog.HandlerOptions{Level: level(cfg.GetLevel())}
	var h slog.Handler
	if strings.EqualFold(cfg.GetFormat(), "json") {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h), closer, nil
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
