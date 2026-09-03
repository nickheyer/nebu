// Package installs records usable copies of runtimes and obtains new ones.
package installs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/eval"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtime"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/transfer"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	probeTimeout = 20 * time.Second
	kindInstall  = "install"
)

// Returned when an install id is not known
var ErrUnknownInstall = errors.New("unknown install")

// Persists installs and obtains new ones
type Manager struct {
	Dir         string
	RuntimesDir string
	Runtimes    *runtime.Registry
	Host        *host.Prober
	Tasks       *tasks.Manager
	Fetcher     *transfer.Fetcher
	Log         *slog.Logger
}

// Lists installs newest first, optionally for one runtime
func (m *Manager) List(runtimeID string) ([]*v1.Install, error) {
	entries, err := os.ReadDir(m.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*v1.Install
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		in, err := m.read(filepath.Join(m.Dir, e.Name()))
		if err != nil {
			m.Log.Warn("skipping install record", "file", e.Name(), "err", err)
			continue
		}
		if runtimeID == "" || in.GetRuntimeId() == runtimeID {
			out = append(out, in)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].GetCreatedAt().AsTime().After(out[j].GetCreatedAt().AsTime())
	})
	return out, nil
}

// Returns one install by id
func (m *Manager) Get(id string) (*v1.Install, error) {
	in, err := m.read(m.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w %q", ErrUnknownInstall, id)
	}
	return in, err
}

// Returns the newest install of a runtime
func (m *Manager) Default(runtimeID string) (*v1.Install, error) {
	list, err := m.List(runtimeID)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%w: no install for runtime %s, adopt or install one", ErrUnknownInstall, runtimeID)
	}
	return list[0], nil
}

// Records a binary already on the host
func (m *Manager) Adopt(ctx context.Context, runtimeID, path string) (*v1.Install, error) {
	rt, err := m.Runtimes.Get(runtimeID)
	if err != nil {
		return nil, err
	}
	if path == "" {
		for _, name := range rt.Manifest.GetAcquire().GetAdopt() {
			if found, err := exec.LookPath(name); err == nil {
				path = found
				break
			}
		}
		if path == "" {
			return nil, fmt.Errorf("none of %v found on PATH, pass a path", rt.Manifest.GetAcquire().GetAdopt())
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(abs); err != nil || info.IsDir() {
		return nil, fmt.Errorf("%s is not an executable file", abs)
	}
	in := &v1.Install{
		Id:        installID(runtimeID, abs),
		RuntimeId: runtimeID,
		Kind:      v1.InstallKind_INSTALL_KIND_ADOPTED,
		Path:      abs,
		Dir:       filepath.Dir(abs),
		Origin:    abs,
		CreatedAt: timestamppb.Now(),
	}
	m.probe(ctx, rt, in)
	return in, m.write(in)
}

// Starts a task that downloads a release matching the host
func (m *Manager) InstallPrebuilt(ctx context.Context, runtimeID string) (*v1.Task, error) {
	rt, err := m.Runtimes.Get(runtimeID)
	if err != nil {
		return nil, err
	}
	profile, err := m.Host.Profile(ctx, false)
	if err != nil {
		return nil, err
	}
	rule, err := m.selectRule(rt, profile)
	if err != nil {
		return nil, err
	}
	title := fmt.Sprintf("install %s prebuilt", runtimeID)
	return m.Tasks.Start(kindInstall, title, map[string]string{"runtime": runtimeID}, func(ctx context.Context, h *tasks.Handle) error {
		return m.download(ctx, h, rt, rule)
	}), nil
}

func (m *Manager) selectRule(rt *runtime.Runtime, profile *v1.HostProfile) (*v1.PrebuiltRule, error) {
	env := host.Env(profile)
	for _, rule := range rt.Manifest.GetAcquire().GetPrebuilt() {
		e, err := eval.Compile(rule.GetWhen())
		if err != nil {
			return nil, err
		}
		ok, err := e.Bool(env)
		if err != nil {
			return nil, err
		}
		if ok {
			return rule, nil
		}
	}
	return nil, fmt.Errorf("no prebuilt release of %s matches this host, adopt a binary instead", rt.Manifest.GetId())
}

type asset struct {
	tag  string
	name string
	url  string
	size int64
}

func (m *Manager) download(ctx context.Context, h *tasks.Handle, rt *runtime.Runtime, rule *v1.PrebuiltRule) error {
	h.Progress(0, 0, "resolving release")
	a, err := m.resolveAsset(ctx, rule)
	if err != nil {
		return err
	}
	h.Logf("release %s asset %s", a.tag, a.name)
	dir := filepath.Join(m.RuntimesDir, rt.Manifest.GetId(), a.tag)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	archive := filepath.Join(dir, a.name)
	if info, err := os.Stat(archive); err != nil || info.Size() != a.size {
		client, err := sources.NewClient(a.url, "")
		if err != nil {
			return err
		}
		h.Progress(0, uint64(a.size), "downloading "+a.name)
		blob := sources.NewRangeBlob(client, a.url, a.size)
		if _, err := m.Fetcher.Fetch(ctx, blob, archive+".partial", "", func(d int64) { h.Add(d) }); err != nil {
			return err
		}
		if err := os.Rename(archive+".partial", archive); err != nil {
			return err
		}
	} else {
		h.Add(a.size)
		h.Logf("archive already downloaded")
	}
	h.Message("extracting")
	if err := extract(archive, dir); err != nil {
		return err
	}
	binary, err := findBinary(dir, rule.GetBinary())
	if err != nil {
		return err
	}
	if err := os.Chmod(binary, 0o755); err != nil {
		return err
	}
	in := &v1.Install{
		Id:        installID(rt.Manifest.GetId(), binary),
		RuntimeId: rt.Manifest.GetId(),
		Kind:      v1.InstallKind_INSTALL_KIND_PREBUILT,
		Path:      binary,
		Dir:       dir,
		Version:   a.tag,
		Origin:    a.url,
		CreatedAt: timestamppb.Now(),
	}
	m.probe(ctx, rt, in)
	if err := m.write(in); err != nil {
		return err
	}
	h.Logf("installed %s at %s", in.GetId(), binary)
	return nil
}

func (m *Manager) resolveAsset(ctx context.Context, rule *v1.PrebuiltRule) (*asset, error) {
	re, err := regexp.Compile(rule.GetAsset())
	if err != nil {
		return nil, err
	}
	client, err := sources.NewClient(rule.GetRelease(), "")
	if err != nil {
		return nil, err
	}
	var root any
	if _, err := client.JSON(ctx, rule.GetRelease(), nil, &root); err != nil {
		return nil, err
	}
	releases, ok := root.([]any)
	if !ok {
		releases = []any{root}
	}
	for _, r := range releases {
		rel, ok := r.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := rel["tag_name"].(string)
		assets, _ := rel["assets"].([]any)
		for _, item := range assets {
			am, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, _ := am["name"].(string)
			if !re.MatchString(name) {
				continue
			}
			url, _ := am["browser_download_url"].(string)
			size, _ := eval.Number(am["size"])
			if tag == "" {
				tag = strings.TrimSuffix(name, filepath.Ext(name))
			}
			return &asset{tag: tag, name: name, url: url, size: int64(size)}, nil
		}
	}
	return nil, fmt.Errorf("no asset matching %s in %s", rule.GetAsset(), rule.GetRelease())
}

// Removes an install record and its downloaded directory
func (m *Manager) Remove(id string) (*v1.Install, error) {
	in, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(m.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if in.GetKind() == v1.InstallKind_INSTALL_KIND_PREBUILT && strings.HasPrefix(in.GetDir(), m.RuntimesDir) {
		if err := os.RemoveAll(in.GetDir()); err != nil {
			return nil, err
		}
	}
	return in, nil
}

// Runs the manifest probes and stores what they capture
func (m *Manager) probe(ctx context.Context, rt *runtime.Runtime, in *v1.Install) {
	in.Facts = map[string]string{}
	for _, p := range rt.Manifest.GetProbes() {
		re, err := regexp.Compile(p.GetMatch())
		if err != nil {
			m.Log.Warn("bad probe pattern", "runtime", rt.Manifest.GetId(), "key", p.GetKey(), "err", err)
			continue
		}
		timeout := probeTimeout
		if p.GetTimeoutMs() > 0 {
			timeout = time.Duration(p.GetTimeoutMs()) * time.Millisecond
		}
		pctx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(pctx, in.GetPath(), p.GetArgs()...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		cmd.Run()
		cancel()
		var values []string
		for _, match := range re.FindAllStringSubmatch(out.String(), -1) {
			value := match[0]
			for i, name := range re.SubexpNames() {
				if name == "value" {
					value = match[i]
				}
			}
			values = append(values, strings.TrimSpace(value))
		}
		if len(values) > 0 {
			in.Facts[p.GetKey()] = strings.Join(values, ",")
		}
	}
	if in.Version == "" {
		in.Version = in.Facts["version"]
	}
}

func (m *Manager) path(id string) string {
	return filepath.Join(m.Dir, id+".json")
}

func (m *Manager) read(path string) (*v1.Install, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	in := &v1.Install{}
	if err := protojson.Unmarshal(data, in); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return in, nil
}

func (m *Manager) write(in *v1.Install) error {
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return err
	}
	data, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(in)
	if err != nil {
		return err
	}
	tmp := m.path(in.GetId()) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path(in.GetId()))
}

func installID(runtimeID, path string) string {
	sum := sha256.Sum256([]byte(path))
	return runtimeID + "-" + hex.EncodeToString(sum[:4])
}

// Finds the named binary anywhere under dir
func findBinary(dir, name string) (string, error) {
	var found string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == name && found == "" {
			found = p
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("%s not found in archive", name)
	}
	return found, nil
}

var _ = http.StatusOK
