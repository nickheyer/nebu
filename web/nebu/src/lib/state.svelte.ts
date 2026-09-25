import { SvelteMap } from 'svelte/reactivity';
import { api, message, unauthenticated } from './api';
import { EventAction, EventKind, type Event } from '$proto/event_pb';
import { DeviceKind, PoolKind, type Device, type HostProfile, type Storage } from '$proto/host_pb';
import { TaskState, type Task } from '$proto/task_pb';
import { InstanceState, type Instance } from '$proto/instance_pb';
import type { Slot } from '$proto/slot_pb';
import { TraceKind, type GatewayStatus, type Route, type Trace } from '$proto/gateway_pb';
import type { Install, RuntimeStatus } from '$proto/runtime_pb';
import type { Format } from '$proto/model_pb';
import type { Build } from '$proto/recipe_pb';
import type { StoredModel, StoreStatus } from '$proto/store_pb';
import { fail, started, settle as settleToast } from './toast.svelte';
import type { Source, SourceStatus } from '$proto/source_pb';
import type { Settings } from '$proto/settings_pb';
import type { Bot, BotActivity } from '$proto/bot_pb';
import { NodeState, FormationState, AdmissionState, type Node, type Formation, type MeshStatus, type StoredSummary } from '$proto/mesh_pb';
import { newestFirst } from './format';
import { weightsName } from './catalog';
import { refreshAuth } from './auth.svelte';

// Match the gateway's per-kind trace limit.
const traceLimit = 500;
const countLimit = 50;
// Activity rows per bot.
const activityLimit = 500;

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
  sources: new SvelteMap<string, Source>(),
  formats: new SvelteMap<string, Format>(),
  traces: new SvelteMap<string, Trace>(),
  bots: new SvelteMap<string, Bot>(),
  // Oldest first, keyed by bot ID.
  botActivity: new SvelteMap<string, BotActivity[]>(),
  // Mesh members, this node among them, and formations conducted anywhere on the mesh
  nodes: new SvelteMap<string, Node>(),
  formations: new SvelteMap<string, Formation>(),
  // The mesh from this node: membership, what is nearby, and admissions under way
  mesh: null as MeshStatus | null
});

// Refresh on connection and after SOURCE, HOST, and INSTALL events.
export const cached = $state({
  loaded: false,
  error: '',
  runtimes: [] as RuntimeStatus[],
  sources: [] as SourceStatus[],
  gateway: null as GatewayStatus | null
});

// Retain successful responses if another service fails.
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

// Coalesce event bursts into one refresh.
function invalidateCached() {
  if (cacheTimer) clearTimeout(cacheTimer);
  cacheTimer = setTimeout(refreshCached, 200);
}

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
  [EventKind.SOURCE]: live.sources,
  [EventKind.BOT]: live.bots,
  [EventKind.NODE]: live.nodes,
  [EventKind.FORMATION]: live.formations
};

// Track snapshot keys to remove stale entries after reconnecting.
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

// Track per-kind counts for constant-time capacity checks.
let answersHeld = 0;
let countsHeld = 0;

// Drop the oldest trace of this kind at capacity.
function keepTrace(t: Trace) {
  const counting = t.kind === TraceKind.COUNT;
  if (!live.traces.has(t.id)) {
    if (counting) countsHeld++;
    else answersHeld++;
  }
  live.traces.set(t.id, t);
  const limit = counting ? countLimit : traceLimit;
  const held = counting ? countsHeld : answersHeld;
  if (held <= limit) return;
  const oldest = [...live.traces.values()].filter((x) => (x.kind === TraceKind.COUNT) === counting).sort((a, b) => Number((a.startedAt?.seconds ?? 0n) - (b.startedAt?.seconds ?? 0n)));
  for (const x of oldest.slice(0, held - limit)) live.traces.delete(x.id);
  if (counting) countsHeld = limit;
  else answersHeld = limit;
}

function sameActivity(a: BotActivity, b: BotActivity): boolean {
  return a.message === b.message && a.kind === b.kind && (a.at?.seconds ?? 0n) === (b.at?.seconds ?? 0n) && (a.at?.nanos ?? 0) === (b.at?.nanos ?? 0);
}

// Merge and deduplicate activity, retaining the newest rows per bot.
export function keepActivity(rows: BotActivity[]) {
  for (const row of rows) {
    const held = live.botActivity.get(row.botId) ?? [];
    if (held.some((x) => sameActivity(x, row))) continue;
    const next = [...held, row].sort((a, b) => Number((a.at?.seconds ?? 0n) - (b.at?.seconds ?? 0n)) || (a.at?.nanos ?? 0) - (b.at?.nanos ?? 0));
    live.botActivity.set(row.botId, next.length > activityLimit ? next.slice(next.length - activityLimit) : next);
  }
}

// Update task toasts when the daemon reports completion.
interface Started {
  toast: number;
  title: string;
  done: string;
  detail?: string;
}
const taskToasts = new Map<string, Started>();
const stopToasts = new Map<string, Started>();

export function startedTask(title: string, done: string, detail: string | undefined, task: Task | undefined) {
  const id = started(title, detail, task ? { href: `/tasks/${task.id}`, label: 'Open task' } : undefined);
  if (!task) return;
  taskToasts.set(task.id, { toast: id, title, done, detail });
  settleTask(live.tasks.get(task.id) ?? task);
}

function settleTask(t: Task) {
  const s = taskToasts.get(t.id);
  if (!s || taskActive(t)) return;
  taskToasts.delete(t.id);
  const link = { href: `/tasks/${t.id}`, linkLabel: 'Open task' };
  if (t.state === TaskState.SUCCEEDED) settleToast(s.toast, { tone: 'ok', title: s.done, detail: s.detail, ...link });
  else if (t.state === TaskState.FAILED) settleToast(s.toast, { tone: 'bad', title: `${s.title} failed`, detail: t.error || s.detail, ...link });
  else settleToast(s.toast, { tone: 'neutral', title: `${s.title} canceled`, detail: s.detail, ...link });
}

export function startedStop(id: string, name: string) {
  stopToasts.set(id, { toast: started(`Stopping ${name}`), title: `Stopping ${name}`, done: `Stopped ${name}` });
  const known = live.instances.get(id);
  if (known) settleStop(known);
}

function settleStop(i: Instance) {
  const s = stopToasts.get(i.id);
  if (!s || instanceLive(i)) return;
  stopToasts.delete(i.id);
  if (i.state === InstanceState.FAILED) settleToast(s.toast, { tone: 'bad', title: `${s.title} failed`, detail: i.error || undefined });
  else settleToast(s.toast, { tone: 'ok', title: s.done });
}

function apply(ev: Event) {
  const p = ev.payload;
  if (ev.kind === EventKind.TASK && p.case === 'task') settleTask(p.value);
  if (ev.kind === EventKind.INSTANCE && p.case === 'instance') settleStop(p.value);
  // Installs can change the available runtime methods.
  if (ev.seq > 0n && (ev.kind === EventKind.HOST || ev.kind === EventKind.SOURCE || ev.kind === EventKind.INSTALL)) invalidateCached();
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
  if (ev.kind === EventKind.TRACE) {
    if (p.case === 'trace') keepTrace(p.value);
    return;
  }
  if (ev.kind === EventKind.BOT_ACTIVITY) {
    if (p.case === 'botActivity') keepActivity([p.value]);
    return;
  }
  if (ev.kind === EventKind.MESH) {
    if (p.case === 'meshStatus') live.mesh = p.value;
    return;
  }
  const map = maps[ev.kind];
  if (!map) return;
  if (ev.action === EventAction.DELETED) {
    map.delete(ev.id);
    return;
  }
  if (p.case && p.value) map.set(ev.id, p.value);
  if (snapshotSeen && ev.seq === 0n) {
    let set = snapshotSeen.get(ev.kind);
    if (!set) snapshotSeen.set(ev.kind, (set = new Set()));
    set.add(ev.id);
  }
}

async function loadTraces(signal: AbortSignal) {
  try {
    const r = await api.gateway.listTraces({ limit: traceLimit }, { signal });
    live.traces.clear();
    answersHeld = countsHeld = 0;
    for (const t of r.traces) keepTrace(t);
  } catch (err) {
    if (!signal.aborted) live.error = message(err);
  }
}

let controller: AbortController | null = null;

// Replay the snapshot and reconnect if the stream closes.
export function connect() {
  controller?.abort();
  controller = new AbortController();
  const signal = controller.signal;
  (async () => {
    let backoff = 500;
    while (!signal.aborted) {
      snapshotSeen = new Map();
      try {
        await refreshAuth();
        const specs = await api.runtimes.listFormats({}, { signal });
        live.formats.clear();
        for (const f of specs.formats) live.formats.set(f.id, f);
        void refreshCached();
        void loadTraces(signal);
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
        if (!cached.loaded) Object.assign(cached, { loaded: true, error: live.error });
      }
      await new Promise((r) => setTimeout(r, backoff));
      backoff = Math.min(backoff * 2, 10000);
    }
  })();
}

export function disconnect() {
  controller?.abort();
  controller = null;
  live.connected = false;
}

export async function probeHost(): Promise<boolean> {
  try {
    const r = await api.host.doctor({});
    startedTask('Probing host', 'Probed host', undefined, r.task);
    return true;
  } catch (err) {
    fail(err, 'Probe failed');
    return false;
  }
}

// Abbreviate the daemon's home directory as ~.
export function homePath(path: string): string {
  const home = live.host?.home;
  if (!home) return path;
  if (path === home) return '~';
  const root = home.endsWith('/') ? home : home + '/';
  return path.startsWith(root) ? '~/' + path.slice(root.length) : path;
}

export function hostName(): string {
  return live.settings?.hostLabel || live.host?.hostname || '';
}

export function hostLabeled(): boolean {
  return !!live.settings?.hostLabel && live.settings.hostLabel !== live.host?.hostname;
}

export async function updateSettings(patch: Partial<Settings>): Promise<boolean> {
  try {
    const r = await api.settings.updateSettings({ settings: { ...(live.settings ?? { hostLabel: '' }), ...patch } as Settings });
    if (r.settings) live.settings = r.settings;
    return true;
  } catch (err) {
    fail(err, 'Save failed');
    return false;
  }
}

export function taskActive(t: Task): boolean {
  return t.state === TaskState.PENDING || t.state === TaskState.RUNNING;
}

const byCreated = newestFirst<{ createdAt?: Task['createdAt'] }>((t) => t.createdAt);

export function activeTasks(): Task[] {
  return [...live.tasks.values()].filter(taskActive).sort(byCreated);
}

// Return the newest task matching the kind and labels.
export function taskFor(kind: string, labels: Record<string, string>, activeOnly = true): Task | undefined {
  const want = Object.entries(labels);
  return [...live.tasks.values()]
    .filter((t) => t.kind === kind && (!activeOnly || taskActive(t)) && want.every(([k, v]) => t.labels[k] === v))
    .sort(byCreated)[0];
}

export function instanceLive(i: Instance | undefined): boolean {
  return !!i && i.state !== InstanceState.STOPPED && i.state !== InstanceState.FAILED;
}

export function liveInstances(): Instance[] {
  return [...live.instances.values()].filter(instanceLive).sort(byCreated);
}

export function orderedSlots(): Slot[] {
  return [...live.slots.values()].sort((a, b) => a.position - b.position || a.name.localeCompare(b.name));
}

export function slotName(id: string | undefined): string {
  if (!id) return '';
  return live.slots.get(id)?.name ?? id;
}

export function slotByRef(ref: string): Slot | undefined {
  return live.slots.get(ref) ?? [...live.slots.values()].find((s) => s.name === ref);
}

export function botByRef(ref: string): Bot | undefined {
  return live.bots.get(ref) ?? [...live.bots.values()].find((b) => b.name === ref);
}

export function botActivityOf(id: string): BotActivity[] {
  return [...(live.botActivity.get(id) ?? [])].reverse();
}

export function sourceName(id: string): string {
  return live.sources.get(id)?.name || id;
}

export function modelKey(m: { sourceId: string; repo: string; group: string }): string {
  return `${m.sourceId}/${m.repo}/${m.group}`;
}

export function runtimeName(id: string): string {
  return cached.runtimes.find((r) => r.runtime?.id === id)?.runtime?.name || id;
}

export function runtimeStatus(id: string): RuntimeStatus | undefined {
  return cached.runtimes.find((r) => r.runtime?.id === id);
}

// Sum planned memory for live instances, excluding the given instance.
export function instancesOnPool(poolId: string, except = ''): bigint {
  let total = 0n;
  for (const i of live.instances.values()) {
    if (i.id === except || !instanceLive(i)) continue;
    for (const p of i.plan?.pools ?? []) if (p.poolId === poolId) total += p.usedBytes;
  }
  return total;
}

export function installsOf(runtimeId: string): Install[] {
  return [...live.installs.values()].filter((i) => i.runtimeId === runtimeId).sort(byCreated);
}

export function formatBlurb(id: string): string {
  const f = live.formats.get(id);
  return f?.blurb || f?.description || `${id} weight files`;
}

export function deviceName(id: string): string {
  return live.host?.devices.find((d) => d.id === id)?.name || id;
}

export function hostGpus(): Device[] {
  return (live.host?.devices ?? []).filter((d) => d.kind !== DeviceKind.CPU);
}

export function poolName(id: string): string {
  const pool = live.host?.pools.find((p) => p.id === id);
  if (!pool) return id;
  if (pool.kind === PoolKind.DEVICE) return deviceName(pool.deviceId);
  return pool.kind === PoolKind.UNIFIED ? 'Unified memory' : 'System memory';
}

export function storeMount(): Storage | undefined {
  const p = live.store?.path ?? '';
  if (!p) return undefined;
  return [...(live.host?.storage ?? [])].filter((s) => p.startsWith(s.path)).sort((a, b) => b.path.length - a.path.length)[0];
}

// Look up the stored format when the record has only a group name.
export function groupLabel(m: { sourceId: string; repo: string; group: string; formatId?: string }): string {
  return weightsName(m.group, m.formatId || live.models.get(modelKey(m))?.formatId);
}

export function tracesOf(route = ''): Trace[] {
  return [...live.traces.values()].filter((t) => !route || t.route === route).sort(newestFirst((t) => t.startedAt));
}

// Exclude token-count requests.
export function answersOf(route = ''): Trace[] {
  return tracesOf(route).filter((t) => t.kind !== TraceKind.COUNT);
}

// Mesh members, this node first, then by name
export function meshNodes(): Node[] {
  return [...live.nodes.values()].sort((a, b) => Number(b.self) - Number(a.self) || a.name.localeCompare(b.name) || a.id.localeCompare(b.id));
}

export function selfNode(): Node | undefined {
  return [...live.nodes.values()].find((n) => n.self);
}

// A member's name, its id when unknown
export function nodeName(id: string | undefined): string {
  if (!id) return '';
  return live.nodes.get(id)?.name || id.slice(0, 8);
}

export function nodeReady(n: Node): boolean {
  return n.self || n.state === NodeState.READY;
}

// Whether this node belongs to a mesh with other members
export function inMesh(): boolean {
  return live.nodes.size > 1;
}

// Whether this node belongs to a mesh at all
export function joinedMesh(): boolean {
  return !!live.mesh?.mesh;
}

// Admissions waiting on a decision here: nodes that asked, on a member; invitations, outside
export function pendingAdmissions(): number {
  return (live.mesh?.admissions ?? []).filter((a) => a.state === AdmissionState.PENDING && (live.mesh?.mesh ? a.asked : a.invited && !a.asked)).length;
}

export function formationLive(f: Formation | undefined): boolean {
  return !!f && f.state !== FormationState.STOPPED && f.state !== FormationState.FAILED;
}

export function liveFormations(): Formation[] {
  return [...live.formations.values()].filter(formationLive).sort(byCreated);
}

export function orderedFormations(): Formation[] {
  return [...live.formations.values()].sort(byCreated);
}

export function formationByRef(ref: string): Formation | undefined {
  return live.formations.get(ref) ?? [...live.formations.values()].filter((f) => f.name === ref).sort((a, b) => Number(formationLive(b)) - Number(formationLive(a)) || Number((b.createdAt?.seconds ?? 0n) - (a.createdAt?.seconds ?? 0n)))[0];
}

// Members holding a stored model
export function holdersOf(m: { sourceId: string; repo: string; group: string }): Node[] {
  return meshNodes().filter((n) => n.stored.some((s) => s.repo === m.repo && s.group === m.group && s.sourceId === m.sourceId));
}

// A model other members hold and this node does not: its summary as the first member lists it,
// and every member holding it, by name
export interface HeldElsewhere {
  key: string;
  summary: StoredSummary;
  holders: Node[];
}

export function heldElsewhere(): HeldElsewhere[] {
  const out = new Map<string, HeldElsewhere>();
  for (const n of meshNodes()) {
    if (n.self) continue;
    for (const s of n.stored) {
      const key = modelKey(s);
      if (live.models.has(key)) continue;
      const row = out.get(key);
      if (row) row.holders.push(n);
      else out.set(key, { key, summary: s, holders: [n] });
    }
  }
  return [...out.values()];
}

// Accelerators on every member, ids node qualified for other nodes and bare for this one, the
// way slot device lists name them
export function meshGpus(): Device[] {
  return meshGpuGroups().flatMap((g) => g.devices);
}

// One group per member holding accelerators, this node first, each device id node qualified
// for other nodes and bare for this one
export interface DeviceGroup {
  nodeId: string;
  node: string;
  self: boolean;
  ready: boolean;
  devices: Device[];
}

export function meshGpuGroups(): DeviceGroup[] {
  const out: DeviceGroup[] = [];
  for (const n of meshNodes()) {
    const devices = (n.profile?.devices ?? []).filter((d) => d.kind !== DeviceKind.CPU).map((d) => ({ ...d, id: n.self ? d.id : `${n.id}/${d.id}` }) as Device);
    if (!devices.length) continue;
    out.push({ nodeId: n.id, node: n.self ? 'This node' : n.name || n.id.slice(0, 8), self: n.self, ready: nodeReady(n), devices });
  }
  return out;
}
