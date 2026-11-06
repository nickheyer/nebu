import { SvelteMap } from 'svelte/reactivity';
import { reason, rpc } from '#lib/api/transport.js';
import type { Bot, BotActivity } from '#lib/proto/nebu/v1/bot_pb.js';
import { EventAction, EventKind, type Event } from '#lib/proto/nebu/v1/event_pb.js';
import type { Objective, Route, SchedulerConfig, Trace } from '#lib/proto/nebu/v1/gateway_pb.js';
import type { HostProfile } from '#lib/proto/nebu/v1/host_pb.js';
import type { Instance } from '#lib/proto/nebu/v1/instance_pb.js';
import type { Formation, MeshStatus, Node } from '#lib/proto/nebu/v1/mesh_pb.js';
import type { Pool } from '#lib/proto/nebu/v1/pool_pb.js';
import type { Build } from '#lib/proto/nebu/v1/recipe_pb.js';
import type { Install } from '#lib/proto/nebu/v1/runtime_pb.js';
import type { Settings } from '#lib/proto/nebu/v1/settings_pb.js';
import type { Slot } from '#lib/proto/nebu/v1/slot_pb.js';
import type { Source } from '#lib/proto/nebu/v1/source_pb.js';
import type { StoredModel, StoreStatus } from '#lib/proto/nebu/v1/store_pb.js';
import type { Task } from '#lib/proto/nebu/v1/task_pb.js';
import type { ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';

// Traces kept newest first for the live traffic views
const traceRing = 2000;
// Activity entries kept per bot
const activityRing = 500;

type Listener = (ev: Event) => void;

function put<T>(map: SvelteMap<string, T>, id: string, value: T, gone: boolean): void {
	if (gone) map.delete(id);
	else map.set(id, value);
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
	return new Promise((resolve) => {
		const timer = setTimeout(resolve, ms);
		signal.addEventListener('abort', () => {
			clearTimeout(timer);
			resolve();
		});
	});
}

export function modelKey(m: { sourceId: string; repo: string; group: string }): string {
	return `${m.sourceId}/${m.repo}${m.group ? `/${m.group}` : ''}`;
}

// Mirrors the daemon's records by subscribing to its event stream with a snapshot on every connection
export class Live {
	connected = $state(false);
	error = $state<string | null>(null);
	// Set once the first snapshot has fully arrived on a connection
	ready = $state(false);
	tasks = new SvelteMap<string, Task>();
	instances = new SvelteMap<string, Instance>();
	slots = new SvelteMap<string, Slot>();
	routes = new SvelteMap<string, Route>();
	installs = new SvelteMap<string, Install>();
	builds = new SvelteMap<string, Build>();
	models = new SvelteMap<string, StoredModel>();
	sources = new SvelteMap<string, Source>();
	bots = new SvelteMap<string, Bot>();
	nodes = new SvelteMap<string, Node>();
	formations = new SvelteMap<string, Formation>();
	toolServers = new SvelteMap<string, ToolServerStatus>();
	pools = new SvelteMap<string, Pool>();
	objectives = new SvelteMap<string, Objective>();
	schedulerConfigs = new SvelteMap<string, SchedulerConfig>();
	botActivity = new SvelteMap<string, BotActivity[]>();
	traces = $state<Trace[]>([]);
	host = $state<HostProfile | undefined>(undefined);
	store = $state<StoreStatus | undefined>(undefined);
	settings = $state<Settings | undefined>(undefined);
	mesh = $state<MeshStatus | undefined>(undefined);
	#abort: AbortController | undefined;
	#listeners = new Map<EventKind, Set<Listener>>();

	start(): void {
		if (this.#abort) return;
		this.#abort = new AbortController();
		void this.#run(this.#abort.signal);
	}

	stop(): void {
		this.#abort?.abort();
		this.#abort = undefined;
		this.connected = false;
		this.ready = false;
	}

	// Calls fn for every event of the kind, answering the call that stops it
	on(kind: EventKind, fn: Listener): () => void {
		let set = this.#listeners.get(kind);
		if (!set) {
			set = new Set();
			this.#listeners.set(kind, set);
		}
		set.add(fn);
		return () => set.delete(fn);
	}

	get activeTasks(): Task[] {
		return [...this.tasks.values()].filter((t) => t.state === 1 || t.state === 2);
	}

	get inMesh(): boolean {
		return this.mesh?.mesh !== undefined;
	}

	async #run(signal: AbortSignal): Promise<void> {
		let delay = 1000;
		while (!signal.aborted) {
			try {
				this.#reset();
				for await (const res of rpc.events.watchEvents({ kinds: [], snapshot: true }, { signal })) {
					this.connected = true;
					this.error = null;
					delay = 1000;
					if (res.event) this.#apply(res.event);
					this.ready = true;
				}
			} catch (err) {
				if (signal.aborted) return;
				this.error = reason(err);
			}
			this.connected = false;
			await sleep(delay, signal);
			delay = Math.min(delay * 2, 30_000);
		}
	}

	#reset(): void {
		this.ready = false;
		for (const map of [
			this.tasks,
			this.instances,
			this.slots,
			this.routes,
			this.installs,
			this.builds,
			this.models,
			this.sources,
			this.bots,
			this.nodes,
			this.formations,
			this.toolServers,
			this.pools,
			this.objectives,
			this.schedulerConfigs,
			this.botActivity
		]) {
			map.clear();
		}
	}

	#apply(ev: Event): void {
		const gone = ev.action === EventAction.DELETED;
		const p = ev.payload;
		switch (p.case) {
			case 'task':
				put(this.tasks, ev.id, p.value, gone);
				break;
			case 'instance':
				put(this.instances, ev.id, p.value, gone);
				break;
			case 'slot':
				put(this.slots, ev.id, p.value, gone);
				break;
			case 'route':
				put(this.routes, ev.id, p.value, gone);
				break;
			case 'install':
				put(this.installs, ev.id, p.value, gone);
				break;
			case 'build':
				put(this.builds, ev.id, p.value, gone);
				break;
			case 'model':
				put(this.models, modelKey(p.value), p.value, gone);
				break;
			case 'source':
				put(this.sources, ev.id, p.value, gone);
				break;
			case 'bot':
				put(this.bots, ev.id, p.value, gone);
				break;
			case 'node':
				put(this.nodes, ev.id, p.value, gone);
				break;
			case 'formation':
				put(this.formations, ev.id, p.value, gone);
				break;
			case 'toolServer':
				put(this.toolServers, ev.id, p.value, gone);
				break;
			case 'pool':
				put(this.pools, ev.id, p.value, gone);
				break;
			case 'objective':
				put(this.objectives, ev.id, p.value, gone);
				break;
			case 'schedulerConfig':
				put(this.schedulerConfigs, ev.id, p.value, gone);
				break;
			case 'botActivity': {
				const list = this.botActivity.get(p.value.botId) ?? [];
				this.botActivity.set(p.value.botId, [p.value, ...list].slice(0, activityRing));
				break;
			}
			case 'trace': {
				const i = this.traces.findIndex((t) => t.id === p.value.id);
				if (i >= 0) this.traces[i] = p.value;
				else this.traces = [p.value, ...this.traces].slice(0, traceRing);
				break;
			}
			case 'host':
				this.host = gone ? undefined : p.value;
				break;
			case 'store':
				this.store = gone ? undefined : p.value;
				break;
			case 'settings':
				this.settings = gone ? undefined : p.value;
				break;
			case 'meshStatus':
				this.mesh = gone ? undefined : p.value;
				break;
			default:
				break;
		}
		this.#listeners.get(ev.kind)?.forEach((fn) => fn(ev));
	}
}

export const live = new Live();
