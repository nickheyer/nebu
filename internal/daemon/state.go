package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/nickheyer/nebu/internal/auth"
	"github.com/nickheyer/nebu/internal/db"
	"github.com/nickheyer/nebu/internal/settings"
	"github.com/nickheyer/nebu/internal/sso"
	"github.com/nickheyer/nebu/pkg/config"
	"github.com/nickheyer/nebu/pkg/events"
	v1 "github.com/nickheyer/nebu/pkg/proto/nebu/v1"
	"github.com/nickheyer/nebu/pkg/runtimes"
	"github.com/nickheyer/nebu/pkg/store"
	"google.golang.org/protobuf/proto"
)

// The wire protocol and relay handoff of each runtime, as the gateway reads them
type runtimeReader struct {
	reg *runtimes.Registry
}

func (r runtimeReader) API(runtimeID string) v1.ApiFlavor { return r.reg.API(runtimeID) }

// The handoff a runtime's shape facts name between prefill and decode seats, empty for none
func (r runtimeReader) Handoff(runtimeID string) string {
	rt, err := r.reg.Get(runtimeID)
	if err != nil {
		return ""
	}
	if p := rt.Policy(); p != nil && p.Shapes != nil {
		return p.Shapes.Handoff
	}
	return ""
}

// Whether a stored model is one a running instance, a slot's relaunch target or a formation still needs
func (d *Daemon) keeps(m *v1.StoredModel) bool {
	same := func(source, repo, group string) bool {
		return source == m.GetSourceId() && repo == m.GetRepo() && group == m.GetGroup()
	}
	for _, in := range d.Instances.List(true) {
		if same(in.GetSourceId(), in.GetRepo(), in.GetGroup()) {
			return true
		}
	}
	for _, s := range d.Slots.List() {
		if r := s.GetRequest(); r != nil && same(r.GetSourceId(), r.GetRepo(), r.GetGroup()) {
			return true
		}
	}
	for _, f := range d.Formations.List(true) {
		if same(f.GetSourceId(), f.GetRepo(), f.GetGroup()) {
			return true
		}
	}
	return false
}

// Names every route profile and bot persona attaching a tool server, so Settings refuses to drop one in use
func (d *Daemon) toolServerUsers(name string) []string {
	var out []string
	for _, r := range d.Pools.Routes() {
		if slices.Contains(r.GetSpec().GetProfile().GetToolServers(), name) {
			out = append(out, "route "+r.GetName())
		}
	}
	return append(out, d.Bots.ToolServerUsers(name)...)
}

// Sets up sessions, the daemon token, user API tokens, and accounts or single sign-on, the guard passing everything under auth.disabled
func (d *Daemon) openAuth(store *db.DB) error {
	cfg, a := d.Config, d.Config.GetAuth()
	if a.GetDisabled() {
		if a.GetToken() != "" || a.GetOidc().GetIssuer() != "" {
			d.Log.Warn("auth.disabled is set, auth.token and auth.oidc are ignored")
		}
		d.Guard = auth.NewGuard("", nil, nil)
		return nil
	}
	ttl, err := time.ParseDuration(a.GetSessionTtl())
	if err != nil || ttl <= 0 {
		return fmt.Errorf("auth.session_ttl %q must be a positive duration such as 24h", a.GetSessionTtl())
	}
	if d.Sessions, err = auth.OpenSessions(filepath.Join(cfg.GetDataDir(), sessionKeyFile), ttl); err != nil {
		return err
	}
	proxies, err := auth.ParseProxies(a.GetTrustedProxies())
	if err != nil {
		return err
	}
	d.Sessions.SetProxies(proxies)
	if a.GetToken() == "" {
		path := filepath.Join(cfg.GetDataDir(), config.TokenFile)
		token, created, err := auth.LoadOrCreateToken(path)
		if err != nil {
			return err
		}
		cfg.Auth.Token = token
		if created {
			d.Log.Info("api token created for CLI and SDK clients", "file", path)
		}
	}
	d.Tokens = auth.NewTokens(store, d.Log)
	if oidc := a.GetOidc(); oidc.GetIssuer() != "" {
		if d.SSO, err = sso.New(oidc, d.Sessions, d.Log); err != nil {
			return err
		}
		d.Log.Info("single sign-on enabled", "issuer", oidc.GetIssuer(), "name", oidc.GetName())
		if !d.SSO.HasAdmins() {
			d.Log.Warn("no single sign-on user signs in as an admin, set auth.oidc.admin_groups, the daemon token administers until then")
		}
	} else {
		d.Users = auth.NewUsers(store, d.Log, d.Tokens)
		if err := d.Users.Load(context.Background()); err != nil {
			return err
		}
		d.Sessions.Validate(d.Users.Valid)
	}
	d.Guard = auth.NewGuard(a.GetToken(), d.Sessions, d.Tokens)
	return nil
}

// Probes the host every few seconds while a runtime runs, else once per profile lifetime, keeping device meters current
func (d *Daemon) watchHost(ctx context.Context) {
	for {
		wait := profileTTL
		if len(d.Instances.List(true)) > 0 {
			wait = liveProbeInterval
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if _, err := d.Host.Profile(ctx, true); err != nil && ctx.Err() == nil {
			d.Log.Warn("host probe failed", "err", err)
		}
	}
}

// Produces the current state of requested kinds as created events, failing when any kind asked for cannot be read
func (d *Daemon) snapshot(ctx context.Context, kinds []v1.EventKind) ([]*v1.Event, error) {
	want := map[v1.EventKind]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	asked := func(kind v1.EventKind) bool { return len(kinds) == 0 || want[kind] }
	var out []*v1.Event
	add := func(kind v1.EventKind, id string, record proto.Message) {
		if asked(kind) {
			out = append(out, events.Event(kind, v1.EventAction_EVENT_ACTION_CREATED, id, record))
		}
	}
	if asked(v1.EventKind_EVENT_KIND_HOST) {
		profile, err := d.Host.Profile(ctx, false)
		if err != nil {
			return nil, fmt.Errorf("snapshot of the host: %w", err)
		}
		add(v1.EventKind_EVENT_KIND_HOST, profile.GetHostname(), profile)
	}
	add(v1.EventKind_EVENT_KIND_SETTINGS, settings.ID, d.Settings.Get())
	for _, st := range d.Tools.Status() {
		add(v1.EventKind_EVENT_KIND_TOOL_SERVER, st.GetServer().GetName(), st)
	}
	for _, s := range d.Sources.List() {
		add(v1.EventKind_EVENT_KIND_SOURCE, s.GetId(), s)
	}
	for _, t := range d.Tasks.List(false) {
		add(v1.EventKind_EVENT_KIND_TASK, t.GetId(), t)
	}
	for _, in := range d.Instances.List(false) {
		add(v1.EventKind_EVENT_KIND_INSTANCE, in.GetId(), in)
	}
	for _, s := range d.Slots.List() {
		add(v1.EventKind_EVENT_KIND_SLOT, s.GetId(), s)
	}
	for _, p := range d.Pools.Pools() {
		add(v1.EventKind_EVENT_KIND_POOL, p.GetId(), p)
	}
	for _, r := range d.Gateway.Routes() {
		add(v1.EventKind_EVENT_KIND_ROUTE, r.GetName(), r)
	}
	for _, o := range d.Pools.Objectives() {
		add(v1.EventKind_EVENT_KIND_OBJECTIVE, o.GetName(), o)
	}
	for _, c := range d.Pools.SchedulerConfigs() {
		add(v1.EventKind_EVENT_KIND_SCHEDULER_CONFIG, c.GetName(), c)
	}
	if d.Mesh.Joined() {
		for _, n := range d.Mesh.Nodes() {
			add(v1.EventKind_EVENT_KIND_NODE, n.GetId(), n)
		}
	}
	add(v1.EventKind_EVENT_KIND_MESH, "mesh", d.Mesh.Status())
	for _, f := range d.Formations.List(false) {
		add(v1.EventKind_EVENT_KIND_FORMATION, f.GetId(), f)
	}
	for _, b := range d.Bots.List() {
		add(v1.EventKind_EVENT_KIND_BOT, b.GetId(), b)
	}
	if asked(v1.EventKind_EVENT_KIND_INSTALL) {
		list, err := d.Installs.List(ctx, "")
		if err != nil {
			return nil, fmt.Errorf("snapshot of installs: %w", err)
		}
		for _, in := range list {
			add(v1.EventKind_EVENT_KIND_INSTALL, in.GetId(), in)
		}
	}
	if asked(v1.EventKind_EVENT_KIND_BUILD) {
		list, err := d.Installs.ListBuilds(ctx, "")
		if err != nil {
			return nil, fmt.Errorf("snapshot of builds: %w", err)
		}
		for _, b := range list {
			add(v1.EventKind_EVENT_KIND_BUILD, b.GetId(), b)
		}
	}
	if asked(v1.EventKind_EVENT_KIND_MODEL) {
		list, err := d.Store.ListManifests()
		if err != nil {
			return nil, fmt.Errorf("snapshot of stored models: %w", err)
		}
		for _, m := range list {
			add(v1.EventKind_EVENT_KIND_MODEL, store.Key(m.GetSourceId(), m.GetRepo(), m.GetGroup()), m)
		}
	}
	if asked(v1.EventKind_EVENT_KIND_STORE) {
		st, err := d.Store.Status()
		if err != nil {
			return nil, fmt.Errorf("snapshot of the store: %w", err)
		}
		add(v1.EventKind_EVENT_KIND_STORE, st.GetPath(), st)
	}
	return out, nil
}
