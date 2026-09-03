import { SvelteMap } from 'svelte/reactivity';
import { api, message } from './api';
import { EventAction, EventKind, type Event } from '$proto/event_pb';
import type { HostProfile } from '$proto/host_pb';
import type { Task } from '$proto/task_pb';
import type { Instance } from '$proto/instance_pb';
import type { Slot } from '$proto/slot_pb';
import type { Route } from '$proto/gateway_pb';
import type { Install } from '$proto/runtime_pb';
import type { Build } from '$proto/recipe_pb';
import type { StoredModel } from '$proto/store_pb';
import type { Finding, Watch } from '$proto/monitor_pb';
import { TaskState } from '$proto/task_pb';

// Everything the UI shows, kept current by the event stream
export const live = $state({
  connected: false,
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

function put<T>(map: SvelteMap<string, T>, id: string, value: T | undefined, action: EventAction) {
  if (action === EventAction.DELETED) map.delete(id);
  else if (value) map.set(id, value);
}

// Folds one event into the state
export function apply(ev: Event) {
  const p = ev.payload;
  switch (ev.kind) {
    case EventKind.HOST:
      if (p.case === 'host') live.host = p.value;
      break;
    case EventKind.TASK:
      put(live.tasks, ev.id, p.case === 'task' ? p.value : undefined, ev.action);
      break;
    case EventKind.INSTANCE:
      put(live.instances, ev.id, p.case === 'instance' ? p.value : undefined, ev.action);
      break;
    case EventKind.SLOT:
      put(live.slots, ev.id, p.case === 'slot' ? p.value : undefined, ev.action);
      break;
    case EventKind.ROUTE:
      put(live.routes, ev.id, p.case === 'route' ? p.value : undefined, ev.action);
      break;
    case EventKind.INSTALL:
      put(live.installs, ev.id, p.case === 'install' ? p.value : undefined, ev.action);
      break;
    case EventKind.BUILD:
      put(live.builds, ev.id, p.case === 'build' ? p.value : undefined, ev.action);
      break;
    case EventKind.MODEL:
      put(live.models, ev.id, p.case === 'model' ? p.value : undefined, ev.action);
      break;
    case EventKind.WATCH:
      put(live.watches, ev.id, p.case === 'watch' ? p.value : undefined, ev.action);
      break;
    case EventKind.FINDING:
      put(live.findings, ev.id, p.case === 'finding' ? p.value : undefined, ev.action);
      break;
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
      try {
        for (const m of [live.tasks, live.instances, live.slots, live.routes, live.installs, live.builds, live.models, live.watches, live.findings]) m.clear();
        for await (const msg of api.events.watchEvents({ snapshot: true }, { signal })) {
          live.connected = true;
          live.error = '';
          backoff = 500;
          if (msg.event) apply(msg.event);
        }
      } catch (err) {
        if (signal.aborted) return;
        live.connected = false;
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

// Lists tasks still running, newest first
export function activeTasks(): Task[] {
  return [...live.tasks.values()].filter((t) => t.state === TaskState.PENDING || t.state === TaskState.RUNNING).sort(byCreated);
}

// Orders by creation time, newest first
export function byCreated(a: { createdAt?: { seconds: bigint } }, b: { createdAt?: { seconds: bigint } }): number {
  return Number((b.createdAt?.seconds ?? 0n) - (a.createdAt?.seconds ?? 0n));
}
