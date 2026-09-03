package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Moves records written as protojson files by earlier builds into the store, then removes them
func (d *DB) ImportLegacy(ctx context.Context, dataDir string, log *slog.Logger) error {
	imported := 0
	n, err := importDir(ctx, filepath.Join(dataDir, "installs"), func() *v1.Install { return &v1.Install{} }, func(in *v1.Install) error { return d.PutInstall(ctx, in) })
	if err != nil {
		return err
	}
	imported += n
	n, err = importDir(ctx, filepath.Join(dataDir, "instances"), func() *v1.Instance { return &v1.Instance{} }, func(in *v1.Instance) error { return d.PutInstance(ctx, in) })
	if err != nil {
		return err
	}
	imported += n
	path := filepath.Join(dataDir, "calibration.json")
	if data, err := os.ReadFile(path); err == nil {
		table := &v1.CalibrationTable{}
		if err := protojson.Unmarshal(data, table); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		for key, c := range table.GetEntries() {
			runtimeID, arch, ok := strings.Cut(key, "|")
			if !ok {
				continue
			}
			if err := d.PutCalibration(ctx, runtimeID, arch, c); err != nil {
				return err
			}
			imported++
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if imported > 0 {
		log.Info("imported records from earlier builds into the store", "records", imported)
	}
	return nil
}

func importDir[T proto.Message](ctx context.Context, dir string, blank func() T, put func(T) error) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return n, err
		}
		msg := blank()
		if err := protojson.Unmarshal(data, msg); err != nil {
			return n, fmt.Errorf("%s: %w", path, err)
		}
		if err := put(msg); err != nil {
			return n, err
		}
		if err := os.Remove(path); err != nil {
			return n, err
		}
		n++
	}
	if n > 0 {
		os.Remove(dir)
	}
	return n, nil
}
