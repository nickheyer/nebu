// Package installs records runtime copies and obtains new ones.
package installs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/archive"
	"github.com/nickheyer/nebu/pkg/build"
	"github.com/nickheyer/nebu/pkg/events"
	"github.com/nickheyer/nebu/pkg/host"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtime"
	"github.com/nickheyer/nebu/pkg/sources"
	"github.com/nickheyer/nebu/pkg/transfer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	probeTimeout = 20 * time.Second
	kindInstall  = "install"
)

// Returned when an install id is not known
var ErrUnknownInstall = errors.New("unknown install")

// Records installs in the store and obtains new ones
type Manager struct {
	DB          *db.DB
	RuntimesDir string
	Runtimes    *runtime.Registry
	Recipes     *build.Registry
	Engine      *build.Engine
	Defaults    *v1.Builds
	Host        *host.Prober
	Tasks       *tasks.Manager
	Fetcher     *transfer.Fetcher
	Sources     *sources.Registry
	Events      *events.Bus
	Log         *slog.Logger

	buildMu  sync.Mutex
	building map[string]*v1.Task
}

func (m *Manager) publishInstall(in *v1.Install, action v1.EventAction) {
	m.Events.Publish(v1.EventKind_EVENT_KIND_INSTALL, action, in.GetId(), in)
}

func (m *Manager) publishBuild(b *v1.Build, action v1.EventAction) {
	m.Events.Publish(v1.EventKind_EVENT_KIND_BUILD, action, b.GetId(), b)
}

// Lists installs newest first, optionally for one runtime
func (m *Manager) List(ctx context.Context, runtimeID string) ([]*v1.Install, error) {
	return m.DB.ListInstalls(ctx, runtimeID)
}

// Returns one install by id
func (m *Manager) Get(ctx context.Context, id string) (*v1.Install, error) {
	in, err := m.DB.GetInstall(ctx, id)
	if db.IsNotFound(err) {
		return nil, fmt.Errorf("%w %q", ErrUnknownInstall, id)
	}
	return in, err
}

// Returns the newest install of a runtime
func (m *Manager) Default(ctx context.Context, runtimeID string) (*v1.Install, error) {
	list, err := m.List(ctx, runtimeID)
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
	if err := m.DB.PutInstall(ctx, in); err != nil {
		return nil, err
	}
	m.publishInstall(in, v1.EventAction_EVENT_ACTION_CREATED)
	return in, nil
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
	rule, err := rt.Prebuilt(profile)
	if err != nil {
		return nil, err
	}
	title := fmt.Sprintf("install %s prebuilt", runtimeID)
	return m.Tasks.Start(kindInstall, title, map[string]string{"runtime": runtimeID}, func(ctx context.Context, h *tasks.Handle) error {
		return m.download(ctx, h, rt, rule)
	}), nil
}

// One release asset a rule matched, opened for download
type asset struct {
	name string
	size int64
	blob sources.Blob
}

// Every asset of one release a rule needs
type release struct {
	tag    string
	assets []asset
}

func (r *release) close() {
	for _, a := range r.assets {
		a.blob.Close()
	}
}

func (m *Manager) download(ctx context.Context, h *tasks.Handle, rt *runtime.Runtime, rule *v1.PrebuiltRule) error {
	h.Progress(0, 0, "resolving release")
	rel, err := m.resolveAssets(ctx, rule)
	if err != nil {
		return err
	}
	defer rel.close()
	dir := filepath.Join(m.RuntimesDir, rt.Manifest.GetId(), rel.tag)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var total uint64
	for _, a := range rel.assets {
		total += uint64(a.size)
		h.Logf("release %s asset %s", rel.tag, a.name)
	}
	h.Progress(0, total, "downloading")
	for _, a := range rel.assets {
		archivePath := filepath.Join(dir, a.name)
		if info, err := os.Stat(archivePath); err == nil && info.Size() == a.size {
			h.Add(a.size)
			h.Logf("%s already downloaded", a.name)
			continue
		}
		h.Message("downloading " + a.name)
		if _, err := m.Fetcher.Fetch(ctx, a.blob, archivePath+".partial", "", func(d int64) { h.Add(d) }); err != nil {
			return err
		}
		if err := os.Rename(archivePath+".partial", archivePath); err != nil {
			return err
		}
	}
	for _, a := range rel.assets {
		h.Message("extracting " + a.name)
		if err := archive.Extract(filepath.Join(dir, a.name), dir); err != nil {
			return err
		}
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
		Version:   rel.tag,
		Origin:    rule.GetReleases() + "@" + rel.tag + "/" + rel.assets[0].name,
		CreatedAt: timestamppb.Now(),
	}
	m.probe(ctx, rt, in)
	if err := m.DB.PutInstall(ctx, in); err != nil {
		return err
	}
	m.publishInstall(in, v1.EventAction_EVENT_ACTION_CREATED)
	h.Logf("installed %s at %s", in.GetId(), binary)
	return nil
}

// Walks the releases of the rule's repository newest first and opens the assets of the first one carrying every pattern
func (m *Manager) resolveAssets(ctx context.Context, rule *v1.PrebuiltRule) (*release, error) {
	patterns := make([]*regexp.Regexp, 0, len(rule.GetAssets()))
	for _, a := range rule.GetAssets() {
		re, err := regexp.Compile(a)
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, re)
	}
	if m.Sources == nil {
		return nil, fmt.Errorf("no sources to read releases from")
	}
	sourceID := rule.GetSource()
	if sourceID == "" {
		sourceID = build.DefaultReleaseSource
	}
	src, err := m.Sources.Get(sourceID)
	if err != nil {
		return nil, err
	}
	revisions, err := src.Revisions(ctx, rule.GetReleases())
	if err != nil {
		return nil, err
	}
	for _, r := range revisions {
		// A revision without bytes carries no assets, branches and tags never do
		if r.GetSizeBytes() == 0 {
			continue
		}
		model, err := src.Resolve(ctx, rule.GetReleases(), r.GetName())
		if err != nil {
			return nil, err
		}
		var picked []*v1.Artifact
		for _, re := range patterns {
			for _, a := range model.GetArtifacts() {
				if re.MatchString(a.GetPath()) {
					picked = append(picked, a)
					break
				}
			}
		}
		if len(picked) != len(patterns) {
			continue
		}
		rel := &release{tag: r.GetName()}
		for _, a := range picked {
			blob, err := src.Open(ctx, model, a)
			if err != nil {
				rel.close()
				return nil, err
			}
			rel.assets = append(rel.assets, asset{name: a.GetPath(), size: blob.Size(), blob: blob})
		}
		return rel, nil
	}
	return nil, fmt.Errorf("no release of %s at %s carries every asset of %s", rule.GetReleases(), sourceID, strings.Join(rule.GetAssets(), ", "))
}

// Removes an install record and its downloaded directory
func (m *Manager) Remove(ctx context.Context, id string) (*v1.Install, error) {
	in, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := m.DB.DeleteInstall(ctx, id); err != nil {
		return nil, err
	}
	if in.GetKind() == v1.InstallKind_INSTALL_KIND_PREBUILT && strings.HasPrefix(in.GetDir(), m.RuntimesDir) {
		if err := os.RemoveAll(in.GetDir()); err != nil {
			return nil, err
		}
	}
	if in.GetKind() == v1.InstallKind_INSTALL_KIND_BUILT && in.GetBuildId() != "" {
		if b, err := m.DB.GetBuild(ctx, in.GetBuildId()); err == nil {
			m.DB.DeleteBuild(ctx, b.GetId())
			if b.GetDir() != "" && m.Engine != nil && strings.HasPrefix(b.GetDir(), m.Engine.Root) {
				os.RemoveAll(b.GetDir())
			}
			m.publishBuild(b, v1.EventAction_EVENT_ACTION_DELETED)
		}
	}
	m.publishInstall(in, v1.EventAction_EVENT_ACTION_DELETED)
	return in, nil
}

// Runs the manifest probes and stores what they capture
func (m *Manager) probe(ctx context.Context, rt *runtime.Runtime, in *v1.Install) {
	in.Facts = map[string]string{}
	for _, p := range rt.Probes() {
		timeout := probeTimeout
		if p.Spec.GetTimeoutMs() > 0 {
			timeout = time.Duration(p.Spec.GetTimeoutMs()) * time.Millisecond
		}
		pctx, cancel := context.WithTimeout(ctx, timeout)
		command, err := rt.ProbeCommand(p, map[string]string{"path": in.GetPath(), "dir": in.GetDir(), "version": in.GetVersion()})
		if err != nil {
			cancel()
			continue
		}
		cmd := exec.CommandContext(pctx, command, p.Spec.GetArgs()...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		cmd.Run()
		cancel()
		var values []string
		for _, match := range p.Match.FindAllStringSubmatch(out.String(), -1) {
			value := match[0]
			for i, name := range p.Match.SubexpNames() {
				if name == "value" {
					value = match[i]
				}
			}
			values = append(values, strings.TrimSpace(value))
		}
		if len(values) > 0 {
			in.Facts[p.Spec.GetKey()] = strings.Join(values, ",")
		}
	}
	if in.Version == "" {
		in.Version = in.Facts["version"]
	}
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
