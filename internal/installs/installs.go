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
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/tasks"
	"github.com/nickheyer/nebu/pkg/archive"
	"github.com/nickheyer/nebu/pkg/build"
	"github.com/nickheyer/nebu/pkg/eval"
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

	buildMu    sync.Mutex
	building   map[string]*v1.Task
	installing map[string]*v1.Task
}

// Writes an install row and tells the stream
func (m *Manager) saveInstall(ctx context.Context, in *v1.Install) error {
	if err := m.DB.PutInstall(ctx, in); err != nil {
		return err
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_INSTALL, v1.EventAction_EVENT_ACTION_CREATED, in.GetId(), in)
	return nil
}

// Drops an install row and tells the stream
func (m *Manager) dropInstall(ctx context.Context, in *v1.Install) error {
	if _, err := m.DB.DeleteInstall(ctx, in.GetId()); err != nil {
		return err
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_INSTALL, v1.EventAction_EVENT_ACTION_DELETED, in.GetId(), in)
	return nil
}

// Writes a build row and tells the stream
func (m *Manager) saveBuild(ctx context.Context, b *v1.Build, action v1.EventAction) error {
	if err := m.DB.PutBuild(ctx, b); err != nil {
		return err
	}
	m.Events.Publish(v1.EventKind_EVENT_KIND_BUILD, action, b.GetId(), b)
	return nil
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
		var names []string
		for _, m := range rt.Installs() {
			names = append(names, m.Spec.GetAdopt().GetNames()...)
		}
		path = onPath(names)
		if path == "" {
			return nil, fmt.Errorf("%w: none of %v found on PATH, pass a path", runtime.ErrParam, names)
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
	if err := m.saveInstall(ctx, in); err != nil {
		return nil, err
	}
	return in, nil
}

// The first of names found on PATH, empty when none is
func onPath(names []string) string {
	for _, name := range names {
		if found, err := exec.LookPath(name); err == nil {
			return found
		}
	}
	return ""
}

// The settings every method reads by name
const (
	fieldPath    = "path"
	fieldBuild   = "build"
	fieldRelease = "release"
	fieldVariant = "variant"
	fieldRef     = "ref"
	fieldSandbox = "sandbox"
	fieldImage   = "image"
	fieldForce   = "force"
	varPrefix    = "var."
)

var sandboxNames = []string{"host", "oci"}

// Describes every install method of a runtime as this host sees it, defaults picked here
func (m *Manager) Options(ctx context.Context, rt *runtime.Runtime, profile *v1.HostProfile) ([]*v1.InstallOption, error) {
	var out []*v1.InstallOption
	for _, im := range rt.Installs() {
		opt := &v1.InstallOption{Method: im.Spec}
		switch how := im.Spec.GetHow().(type) {
		case *v1.InstallMethod_Adopt:
			found := onPath(how.Adopt.GetNames())
			opt.Fields = []*v1.ConfigField{{Name: fieldPath, Label: "Binary", Type: v1.ConfigType_CONFIG_TYPE_PATH, Required: found == "", Default: found, Description: "Path of " + strings.Join(how.Adopt.GetNames(), " or ")}}
			if found == "" {
				opt.Unmet = []string{"none of " + strings.Join(how.Adopt.GetNames(), ", ") + " on PATH"}
			}
		case *v1.InstallMethod_Prebuilt:
			// Only the builds published for this host are offered, the first of them by default
			rules, err := im.Rules(profile)
			if err != nil {
				return nil, err
			}
			var ids []string
			for _, r := range rules {
				ids = append(ids, r.GetId())
			}
			opt.Fields = []*v1.ConfigField{
				{Name: fieldBuild, Label: "Build", Type: v1.ConfigType_CONFIG_TYPE_STRING, Required: true, Default: first(ids), Choices: ids, Description: "Which published build to download"},
				{Name: fieldRelease, Label: "Release", Type: v1.ConfigType_CONFIG_TYPE_STRING, Description: "A release tag of " + how.Prebuilt.GetReleases() + ", the newest with the build when empty"},
			}
			if len(rules) == 0 {
				opt.Unmet = []string{"no build of " + how.Prebuilt.GetReleases() + " is published for " + profile.GetOs() + "/" + profile.GetArch()}
			}
		case *v1.InstallMethod_Recipe:
			rc, err := m.Recipes.Get(how.Recipe.GetRecipeId())
			if err != nil {
				return nil, err
			}
			sel, err := rc.Select(profile, build.Options{Defaults: m.Defaults})
			if err != nil {
				opt.Unmet = []string{err.Error()}
				opt.Recipe = &v1.RecipeStatus{Recipe: rc.Spec, Unmet: opt.Unmet}
				break
			}
			opt.Recipe = sel.Status()
			opt.Unmet = sel.Unmet
			// Only the variants this host can take are offered
			applicable, err := rc.Variants(profile)
			if err != nil {
				return nil, err
			}
			var variants []string
			for _, v := range applicable {
				variants = append(variants, v.GetId())
			}
			opt.Fields = []*v1.ConfigField{
				{Name: fieldRef, Label: "Ref", Type: v1.ConfigType_CONFIG_TYPE_STRING, Default: sel.Ref, Description: "A tag, branch, or commit of the source"},
				{Name: fieldSandbox, Label: "Sandbox", Type: v1.ConfigType_CONFIG_TYPE_STRING, Default: eval.EnumShort(sel.Sandbox), Choices: sandboxNames, Description: "The host toolchain, or a container through " + firstOr(sel.CLI, "a container cli")},
				{Name: fieldImage, Label: "Image", Type: v1.ConfigType_CONFIG_TYPE_STRING, Default: sel.Image, Description: "Container image holding the toolchain, for the oci sandbox"},
				{Name: fieldForce, Label: "Rebuild", Type: v1.ConfigType_CONFIG_TYPE_BOOL, Default: "false", Description: "Build again even when this exact build exists"},
			}
			if len(variants) > 0 {
				opt.Fields = append([]*v1.ConfigField{{Name: fieldVariant, Label: "Variant", Type: v1.ConfigType_CONFIG_TYPE_STRING, Required: true, Default: sel.Variant.GetId(), Choices: variants, Description: "Which variant of the recipe to build"}}, opt.Fields...)
			}
			for _, k := range slices.Sorted(maps.Keys(sel.Vars)) {
				opt.Fields = append(opt.Fields, &v1.ConfigField{Name: varPrefix + k, Label: k, Type: v1.ConfigType_CONFIG_TYPE_STRING, Default: sel.Vars[k], Description: "Recipe variable"})
			}
		}
		out = append(out, opt)
	}
	return out, nil
}

func firstOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func first(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return items[0]
}

// What one install does once its settings are read
type installPlan struct {
	method  *v1.InstallMethod
	path    string
	rule    *v1.PrebuiltRule
	release string
	build   *v1.BuildRequest
}

// Reads a method's settings into a plan, refusing names the method does not take
func (m *Manager) plan(ctx context.Context, rt *runtime.Runtime, profile *v1.HostProfile, method string, settings map[string]string) (*installPlan, error) {
	im, err := rt.Install(method)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, name := range []string{fieldPath, fieldBuild, fieldRelease, fieldVariant, fieldRef, fieldSandbox, fieldImage, fieldForce} {
		known[name] = true
	}
	for k := range settings {
		if !known[k] && !strings.HasPrefix(k, varPrefix) {
			return nil, fmt.Errorf("%w: install method %s takes no setting %q", runtime.ErrParam, method, k)
		}
	}
	p := &installPlan{method: im.Spec}
	switch how := im.Spec.GetHow().(type) {
	case *v1.InstallMethod_Adopt:
		p.path = strings.TrimSpace(settings[fieldPath])
		if p.path == "" {
			p.path = onPath(how.Adopt.GetNames())
		}
		if p.path == "" {
			return nil, fmt.Errorf("%w: none of %s on PATH, set path", runtime.ErrParam, strings.Join(how.Adopt.GetNames(), ", "))
		}
	case *v1.InstallMethod_Prebuilt:
		if id := strings.TrimSpace(settings[fieldBuild]); id != "" {
			if p.rule, err = im.Rule(id); err != nil {
				return nil, err
			}
		} else if p.rule, err = im.HostRule(profile); err != nil {
			return nil, err
		} else if p.rule == nil {
			return nil, fmt.Errorf("%w: no build of %s is published for %s/%s, set build", runtime.ErrParam, how.Prebuilt.GetReleases(), profile.GetOs(), profile.GetArch())
		}
		p.release = strings.TrimSpace(settings[fieldRelease])
	case *v1.InstallMethod_Recipe:
		p.build = &v1.BuildRequest{RuntimeId: rt.Manifest.GetId(), RecipeId: how.Recipe.GetRecipeId(), Variant: settings[fieldVariant], Ref: settings[fieldRef], Image: settings[fieldImage], Vars: map[string]string{}}
		switch strings.ToLower(strings.TrimSpace(settings[fieldSandbox])) {
		case "":
		case "host":
			p.build.Sandbox = v1.SandboxKind_SANDBOX_KIND_HOST
		case "oci":
			p.build.Sandbox = v1.SandboxKind_SANDBOX_KIND_OCI
		default:
			return nil, fmt.Errorf("%w: sandbox %q, one of %s", runtime.ErrParam, settings[fieldSandbox], strings.Join(sandboxNames, ", "))
		}
		if v := strings.TrimSpace(settings[fieldForce]); v != "" {
			if p.build.Force, err = strconv.ParseBool(v); err != nil {
				return nil, fmt.Errorf("%w: force: %v", runtime.ErrParam, err)
			}
		}
		for k, v := range settings {
			if strings.HasPrefix(k, varPrefix) {
				p.build.Vars[strings.TrimPrefix(k, varPrefix)] = v
			}
		}
		// Bad settings are refused now, before anything runs
		if _, _, err := m.resolveBuild(ctx, p.build); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// Installs a runtime as one task by the method and settings chosen
//
// A second install of a runtime whose install is under way returns that task.
func (m *Manager) Install(ctx context.Context, runtimeID, method string, settings map[string]string) (*v1.Task, error) {
	rt, err := m.Runtimes.Get(runtimeID)
	if err != nil {
		return nil, err
	}
	profile, err := m.Host.Profile(ctx, false)
	if err != nil {
		return nil, err
	}
	if ok, unmet := rt.Compatible(profile); !ok {
		return nil, fmt.Errorf("%w: %s needs %s", runtime.ErrParam, runtimeID, strings.Join(unmet, ", "))
	}
	p, err := m.plan(ctx, rt, profile, method, settings)
	if err != nil {
		return nil, err
	}
	m.buildMu.Lock()
	defer m.buildMu.Unlock()
	if task, ok := m.installing[runtimeID]; ok {
		if t, _, err := m.Tasks.Get(task.GetId()); err == nil && !terminal(t.GetState()) {
			return t, nil
		}
	}
	task := m.Tasks.Start(kindInstall, "install "+rt.Manifest.GetName(), map[string]string{"runtime": runtimeID, "method": method}, func(ctx context.Context, h *tasks.Handle) error {
		defer func() {
			m.buildMu.Lock()
			delete(m.installing, runtimeID)
			m.buildMu.Unlock()
		}()
		return m.install(ctx, h, rt, p)
	})
	if m.installing == nil {
		m.installing = map[string]*v1.Task{}
	}
	m.installing[runtimeID] = task
	return task, nil
}

func terminal(s v1.TaskState) bool {
	return s == v1.TaskState_TASK_STATE_SUCCEEDED || s == v1.TaskState_TASK_STATE_FAILED || s == v1.TaskState_TASK_STATE_CANCELED
}

// Carries out one install plan
func (m *Manager) install(ctx context.Context, h *tasks.Handle, rt *runtime.Runtime, p *installPlan) error {
	switch how := p.method.GetHow().(type) {
	case *v1.InstallMethod_Adopt:
		h.Logf("adopting %s", p.path)
		in, err := m.Adopt(ctx, rt.Manifest.GetId(), p.path)
		if err != nil {
			return err
		}
		h.Logf("adopted %s at %s", in.GetVersion(), in.GetPath())
		h.Progress(1, 1, "adopted "+in.GetPath())
		return nil
	case *v1.InstallMethod_Prebuilt:
		h.Logf("downloading build %s of %s", p.rule.GetId(), how.Prebuilt.GetReleases())
		return m.download(ctx, h, rt, how.Prebuilt, p.rule, p.release)
	case *v1.InstallMethod_Recipe:
		h.Logf("building recipe %s", how.Recipe.GetRecipeId())
		return m.buildInline(ctx, h, p.build)
	}
	return fmt.Errorf("install method %s does nothing", p.method.GetId())
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

func (m *Manager) download(ctx context.Context, h *tasks.Handle, rt *runtime.Runtime, pre *v1.Prebuilt, rule *v1.PrebuiltRule, tag string) error {
	h.Progress(0, 0, "resolving release")
	rel, err := m.resolveAssets(ctx, pre, rule, tag)
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
		h.Message("downloading " + a.name)
		reused, err := m.Fetcher.Land(ctx, a.blob, filepath.Join(dir, a.name), func(d int64) { h.Add(d) })
		if err != nil {
			return err
		}
		if reused {
			h.Logf("%s already downloaded", a.name)
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
		Origin:    pre.GetReleases() + "@" + rel.tag + "/" + rel.assets[0].name,
		CreatedAt: timestamppb.Now(),
	}
	m.probe(ctx, rt, in)
	if err := m.saveInstall(ctx, in); err != nil {
		return err
	}
	h.Logf("installed %s at %s", in.GetId(), binary)
	return nil
}

// Opens the rule's assets from the release named, or from the newest release carrying every one of them
func (m *Manager) resolveAssets(ctx context.Context, pre *v1.Prebuilt, rule *v1.PrebuiltRule, tag string) (*release, error) {
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
	sourceID := pre.GetSource()
	if sourceID == "" {
		sourceID = build.DefaultReleaseSource
	}
	src, err := m.Sources.Get(sourceID)
	if err != nil {
		return nil, err
	}
	var revisions []*v1.Revision
	if tag != "" {
		revisions = []*v1.Revision{{Name: tag, SizeBytes: 1}}
	} else if revisions, err = src.Revisions(ctx, pre.GetReleases()); err != nil {
		return nil, err
	}
	for _, r := range revisions {
		// A revision without bytes carries no assets, branches and tags never do
		if r.GetSizeBytes() == 0 {
			continue
		}
		model, err := src.Resolve(ctx, pre.GetReleases(), r.GetName())
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
			if tag != "" {
				return nil, fmt.Errorf("release %s of %s lacks an asset for %s", tag, pre.GetReleases(), strings.Join(rule.GetAssets(), ", "))
			}
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
	return nil, fmt.Errorf("no release of %s at %s carries every asset of %s", pre.GetReleases(), sourceID, strings.Join(rule.GetAssets(), ", "))
}

// Removes an install record and its downloaded directory
func (m *Manager) Remove(ctx context.Context, id string) (*v1.Install, error) {
	in, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := m.dropInstall(ctx, in); err != nil {
		return nil, err
	}
	if in.GetKind() == v1.InstallKind_INSTALL_KIND_PREBUILT && strings.HasPrefix(in.GetDir(), m.RuntimesDir) {
		if err := os.RemoveAll(in.GetDir()); err != nil {
			return nil, err
		}
	}
	if in.GetKind() == v1.InstallKind_INSTALL_KIND_BUILT && in.GetBuildId() != "" {
		if b, err := m.DB.GetBuild(ctx, in.GetBuildId()); err == nil {
			m.dropBuild(ctx, b)
		}
	}
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
