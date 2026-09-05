import { SvelteMap } from 'svelte/reactivity';
import { api, message, unauthenticated } from './api';
import { EventAction, EventKind, type Event } from '$proto/event_pb';
import type { HostProfile } from '$proto/host_pb';
import { TaskState, type Task } from '$proto/task_pb';
import { InstanceState, type Instance } from '$proto/instance_pb';
import type { Slot } from '$proto/slot_pb';
import type { GatewayStatus, Route } from '$proto/gateway_pb';
import type { Install, Profile, RuntimeStatus } from '$proto/runtime_pb';
import type { FormatSpec } from '$proto/model_pb';
import type { Build } from '$proto/recipe_pb';
import type { StoredModel, StoreStatus } from '$proto/store_pb';
import { FindingKind, type Finding, type Watch, type Want } from '$proto/monitor_pb';
import { fail, toast } from './toast.svelte';
import type { Source, SourceStatus } from '$proto/source_pb';
import type { Settings } from '$proto/settings_pb';
import { byName, newestFirst } from './format';

// Everything the UI shows, kept current by the event stream
export const live = $state({
  connected: false,
  ready: false,
  needsToken: false,
  error: '',
  host: null as HostProfile | null,
  settings: null as Settings | null,
  store: null as StoreStatus | null,
  tasks: new SvelteMap<string, Task>(),
  instances: new SvelteMap<string, Instance>(),
  slots: new SvelteMap<string, Slot>(),
  routes: new SvelteMap<string, Route>(),
  installs: new SvelteMap<string, Install>(),
  builds: new SvelteMap<string, Build>(),
  models: new SvelteMap<string, StoredModel>(),
  watches: new SvelteMap<string, Watch>(),
  findings: new SvelteMap<string, Finding>(),
  sources: new SvelteMap<string, Source>(),
  profiles: new SvelteMap<string, Profile>(),
  formats: new SvelteMap<string, FormatSpec>(),
  wants: new SvelteMap<string, Want>()
});

// Lists without a map, reread per connection and after SOURCE or HOST events
export const cached = $state({
  loaded: false,
  error: '',
  runtimes: [] as RuntimeStatus[],
  sources: [] as SourceStatus[],
  gateway: null as GatewayStatus | null
});

// Reads the runtimes, source statuses, and gateway status, keeping what still answers when one fails
export async function refreshCached() {
  const [r, s, g] = await Promise.allSettled([api.runtimes.listRuntimes({}), api.sources.listSources({}), api.gateway.getGatewayStatus({})]);
  if (r.status === 'fulfilled') cached.runtimes = r.value.runtimes;
  if (s.status === 'fulfilled') cached.sources = s.value.sources;
  if (g.status === 'fulfilled') cached.gateway = g.value.status ?? null;
  const failed = [s, r, g].find((x): x is PromiseRejectedResult => x.status === 'rejected');
  cached.error = failed ? message(failed.reason) : '';
  cached.loaded = true;
}

let cacheTimer: ReturnType<typeof setTimeout> | null = null;

// Coalesces a burst of events into one refresh
function invalidateCached() {
  if (cacheTimer) clearTimeout(cacheTimer);
  cacheTimer = setTimeout(refreshCached, 200);
}

// A clock that ticks so relative times stay fresh
export const clock = $state({ now: Date.now() });
if (typeof window !== 'undefined') setInterval(() => (clock.now = Date.now()), 5000);

type Maps = {
  [K in EventKind]?: SvelteMap<string, unknown>;
};

const maps: Maps = {
  [EventKind.TASK]: live.tasks,
  [EventKind.INSTANCE]: live.instances,
  [EventKind.SLOT]: live.slots,
  [EventKind.ROUTE]: live.routes,
  [EventKind.INSTALL]: live.installs,
  [EventKind.BUILD]: live.builds,
  [EventKind.MODEL]: live.models,
  [EventKind.WATCH]: live.watches,
  [EventKind.FINDING]: live.findings,
  [EventKind.SOURCE]: live.sources,
  [EventKind.PROFILE]: live.profiles,
  [EventKind.WANT]: live.wants
};

const notifyKey = 'nebu.notify';

// Whether this browser raises desktop notifications for findings
export function desktopNotify(): boolean {
  try {
    return localStorage.getItem(notifyKey) === '1';
  } catch {
    return false;
  }
}

export async function setDesktopNotify(on: boolean): Promise<boolean> {
  if (on && typeof Notification !== 'undefined' && Notification.permission !== 'granted') {
    if ((await Notification.requestPermission()) !== 'granted') return false;
  }
  try {
    localStorage.setItem(notifyKey, on ? '1' : '0');
  } catch {
    // storage may be unavailable
  }
  return on;
}

// Announces a finding that arrived live, in a toast and on the desktop when allowed
function announce(f: Finding) {
  const title = f.kind === FindingKind.WANTED_FOUND ? `Found ${f.repo}` : `${f.repo} changed`;
  toast({ tone: f.kind === FindingKind.REMOVED_GROUP ? 'warn' : 'info', title, detail: f.detail, href: '/monitor', linkLabel: 'Monitor', sticky: true });
  if (desktopNotify() && typeof Notification !== 'undefined' && Notification.permission === 'granted') {
    try {
      new Notification(title, { body: f.detail, tag: f.id });
    } catch {
      // some browsers refuse notifications from a page without a service worker
    }
  }
}

// Keys seen during a snapshot so entries gone while disconnected can be pruned
let snapshotSeen: Map<EventKind, Set<string>> | null = null;
let settleTimer: ReturnType<typeof setTimeout> | null = null;

function settle() {
  if (!snapshotSeen) return;
  for (const [kindText, map] of Object.entries(maps)) {
    const kind = Number(kindText) as EventKind;
    const seen = snapshotSeen.get(kind) ?? new Set<string>();
    for (const id of [...map!.keys()]) if (!seen.has(id)) map!.delete(id);
  }
  snapshotSeen = null;
  live.ready = true;
}

// Folds one event into the state
function apply(ev: Event) {
  const p = ev.payload;
  if (ev.seq > 0n && (ev.kind === EventKind.HOST || ev.kind === EventKind.SOURCE)) invalidateCached();
  if (ev.kind === EventKind.HOST) {
    if (p.case === 'host') live.host = p.value;
    return;
  }
  if (ev.kind === EventKind.STORE) {
    if (p.case === 'store') live.store = p.value;
    return;
  }
  if (ev.kind === EventKind.SETTINGS) {
    if (p.case === 'settings') live.settings = p.value;
    return;
  }
  const map = maps[ev.kind];
  if (!map) return;
  if (ev.action === EventAction.DELETED) {
    map.delete(ev.id);
    return;
  }
  if (p.case && p.value) map.set(ev.id, p.value);
  if (p.case === 'finding' && ev.action === EventAction.CREATED && ev.seq > 0n) announce(p.value);
  if (snapshotSeen && ev.seq === 0n) {
    let set = snapshotSeen.get(ev.kind);
    if (!set) snapshotSeen.set(ev.kind, (set = new Set()));
    set.add(ev.id);
  }
}

let controller: AbortController | null = null;

// Subscribes to the daemon, replaying the snapshot, reconnecting on loss
export function connect() {
  controller?.abort();
  controller = new AbortController();
  const signal = controller.signal;
  (async () => {
    let backoff = 500;
    while (!signal.aborted) {
      snapshotSeen = new Map();
      try {
        // Formats are spec data with no events, so each connection reads them once
        const specs = await api.runtimes.listFormats({}, { signal });
        live.formats.clear();
        for (const f of specs.formats) live.formats.set(f.id, f);
        void refreshCached();
        for await (const msg of api.events.watchEvents({ snapshot: true }, { signal })) {
          live.connected = true;
          live.needsToken = false;
          live.error = '';
          backoff = 500;
          if (!msg.event) continue;
          apply(msg.event);
          if (snapshotSeen) {
            if (msg.event.seq > 0n) settle();
            else {
              if (settleTimer) clearTimeout(settleTimer);
              settleTimer = setTimeout(settle, 400);
            }
          }
        }
      } catch (err) {
        if (signal.aborted) return;
        live.connected = false;
        live.needsToken = unauthenticated(err);
        live.error = message(err);
        // Pages waiting on the cached lists get the same answer instead of a skeleton
        if (!cached.loaded) Object.assign(cached, { loaded: true, error: live.error });
      }
      await new Promise((r) => setTimeout(r, backoff));
      backoff = Math.min(backoff * 2, 10000);
    }
  })();
}

// Stops the subscription
export function disconnect() {
  controller?.abort();
  controller = null;
  live.connected = false;
}

// Probes the host again, the HOST event carrying the new profile everywhere else
export async function probeHost() {
  try {
    const resp = await api.host.getProfile({ refresh: true });
    if (resp.profile) live.host = resp.profile;
  } catch (err) {
    fail(err, 'Probe failed');
  }
}

// What this host is called: its label, else its hostname
export function hostName(): string {
  return live.settings?.hostLabel || live.host?.hostname || '';
}

// Writes settings, the stream carrying the change back to every page
export async function updateSettings(patch: Partial<Settings>): Promise<boolean> {
  try {
    const r = await api.settings.updateSettings({ settings: { ...(live.settings ?? { hostLabel: '', setupDismissed: false }), ...patch } as Settings });
    if (r.settings) live.settings = r.settings;
    return true;
  } catch (err) {
    fail(err, 'Could not save');
    return false;
  }
}

export function taskActive(t: Task): boolean {
  return t.state === TaskState.PENDING || t.state === TaskState.RUNNING;
}

const byCreated = newestFirst<{ createdAt?: Task['createdAt'] }>((t) => t.createdAt);

// Lists tasks still running, newest first
export function activeTasks(): Task[] {
  return [...live.tasks.values()].filter(taskActive).sort(byCreated);
}

// Finds the newest task of a kind whose labels include every given pair
export function taskFor(kind: string, labels: Record<string, string>, activeOnly = true): Task | undefined {
  const want = Object.entries(labels);
  return [...live.tasks.values()]
    .filter((t) => t.kind === kind && (!activeOnly || taskActive(t)) && want.every(([k, v]) => t.labels[k] === v))
    .sort(byCreated)[0];
}

export function instanceLive(i: Instance | undefined): boolean {
  return !!i && i.state !== InstanceState.STOPPED && i.state !== InstanceState.FAILED;
}

// Instances that are alive, newest first
export function liveInstances(): Instance[] {
  return [...live.instances.values()].filter(instanceLive).sort(byCreated);
}

export function slotName(id: string | undefined): string {
  if (!id) return '';
  return live.slots.get(id)?.name ?? id;
}

export function modelKey(m: { sourceId: string; repo: string; group: string }): string {
  return `${m.sourceId}/${m.repo}/${m.group}`;
}

export function unackedFindings(): Finding[] {
  return [...live.findings.values()].filter((f) => !f.acknowledged && f.kind !== FindingKind.UNSPECIFIED);
}

// Profiles of one runtime, the default first, or every profile by runtime when none is named
export function profilesOf(runtimeId: string): Profile[] {
  return [...live.profiles.values()]
    .filter((p) => !runtimeId || p.runtimeId === runtimeId)
    .sort(byName((p) => `${p.runtimeId} ${p.default ? 0 : 1} ${p.name}`));
}

// Params a run of a runtime starts from: the named profile, else the runtime default
export function profileParams(runtimeId: string, profileId = ''): Record<string, string> {
  const p = profileId ? live.profiles.get(profileId) : profilesOf(runtimeId).find((p) => p.default);
  return p?.runtimeId === runtimeId ? { ...p.params } : {};
}

// What a weight format is, in the words its spec carries
export function formatBlurb(id: string): string {
  const f = live.formats.get(id);
  return f?.blurb || f?.description || `${id} weight files`;
}
