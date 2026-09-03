import { SvelteMap } from 'svelte/reactivity';
import { api, message, unauthenticated } from './api';
import { EventAction, EventKind, type Event } from '$proto/event_pb';
import type { HostProfile } from '$proto/host_pb';
import { TaskState, type Task } from '$proto/task_pb';
import { InstanceState, type Instance } from '$proto/instance_pb';
import type { Slot } from '$proto/slot_pb';
import type { Route } from '$proto/gateway_pb';
import type { Install } from '$proto/runtime_pb';
import type { Build } from '$proto/recipe_pb';
import type { StoredModel } from '$proto/store_pb';
import { FindingKind, type Finding, type Watch } from '$proto/monitor_pb';
import { newestFirst } from './format';

// Everything the UI shows, kept current by the event stream
export const live = $state({
  connected: false,
  ready: false,
  needsToken: false,
  error: '',
  host: null as HostProfile | null,
  tasks: new SvelteMap<string, Task>(),
  instances: new SvelteMap<string, Instance>(),
  slots: new SvelteMap<string, Slot>(),
  routes: new SvelteMap<string, Route>(),
  installs: new SvelteMap<string, Install>(),
  builds: new SvelteMap<string, Build>(),
  models: new SvelteMap<string, StoredModel>(),
  watches: new SvelteMap<string, Watch>(),
  findings: new SvelteMap<string, Finding>()
});

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
  [EventKind.FINDING]: live.findings
};

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
export function apply(ev: Event) {
  const p = ev.payload;
  if (ev.kind === EventKind.HOST) {
    if (p.case === 'host') live.host = p.value;
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

// Refreshes the host profile from the daemon
export async function refreshHost(refresh = false) {
  const resp = await api.host.getProfile({ refresh });
  if (resp.profile) live.host = resp.profile;
}

export function taskActive(t: Task): boolean {
  return t.state === TaskState.PENDING || t.state === TaskState.RUNNING;
}

// Lists tasks still running, newest first
export function activeTasks(): Task[] {
  return [...live.tasks.values()].filter(taskActive).sort(newestFirst);
}

// Finds the newest task of a kind whose labels include every given pair
export function taskFor(kind: string, labels: Record<string, string>, activeOnly = true): Task | undefined {
  const want = Object.entries(labels);
  return [...live.tasks.values()]
    .filter((t) => t.kind === kind && (!activeOnly || taskActive(t)) && want.every(([k, v]) => t.labels[k] === v))
    .sort(newestFirst)[0];
}

export function instanceLive(i: Instance | undefined): boolean {
  return !!i && i.state !== InstanceState.STOPPED && i.state !== InstanceState.FAILED;
}

// Instances that are alive, newest first
export function liveInstances(): Instance[] {
  return [...live.instances.values()].filter(instanceLive).sort(newestFirst);
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
