import { SvelteMap } from 'svelte/reactivity';
import { reason, rpc } from '#lib/api/transport.js';
import { EventAction, EventKind, type Event } from '#lib/proto/nebu/v1/event_pb.js';
import type { HostProfile } from '#lib/proto/nebu/v1/host_pb.js';
import type { Instance } from '#lib/proto/nebu/v1/instance_pb.js';
import type { Install } from '#lib/proto/nebu/v1/runtime_pb.js';
import type { Settings } from '#lib/proto/nebu/v1/settings_pb.js';
import type { Slot } from '#lib/proto/nebu/v1/slot_pb.js';
import type { Source } from '#lib/proto/nebu/v1/source_pb.js';
import type { StoredModel, StoreStatus } from '#lib/proto/nebu/v1/store_pb.js';
import type { Task } from '#lib/proto/nebu/v1/task_pb.js';

const kinds = [
	EventKind.HOST,
	EventKind.TASK,
	EventKind.INSTANCE,
	EventKind.SLOT,
	EventKind.INSTALL,
	EventKind.MODEL,
	EventKind.SOURCE,
	EventKind.STORE,
	EventKind.SETTINGS
];

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

// Mirrors the daemon's records by subscribing to its event stream with a snapshot on every connection
export class Live {
	connected = $state(false);
	error = $state<string | null>(null);
	tasks = new SvelteMap<string, Task>();
	instances = new SvelteMap<string, Instance>();
	installs = new SvelteMap<string, Install>();
	slots = new SvelteMap<string, Slot>();
	sources = new SvelteMap<string, Source>();
	models = $state<StoredModel[]>([]);
	host = $state<HostProfile | undefined>(undefined);
	store = $state<StoreStatus | undefined>(undefined);
	settings = $state<Settings | undefined>(undefined);
	#abort: AbortController | undefined;
	#modelsTimer: ReturnType<typeof setTimeout> | undefined;

	start(): void {
		if (this.#abort) return;
		this.#abort = new AbortController();
		void this.#run(this.#abort.signal);
	}

	stop(): void {
		this.#abort?.abort();
		this.#abort = undefined;
		this.connected = false;
	}

	async refreshModels(): Promise<void> {
		const res = await rpc.store.listModels({});
		this.models = res.models;
	}

	async #run(signal: AbortSignal): Promise<void> {
		let delay = 1000;
		while (!signal.aborted) {
			try {
				this.#reset();
				await this.refreshModels();
				for await (const res of rpc.events.watchEvents({ kinds, snapshot: true }, { signal })) {
					this.connected = true;
					this.error = null;
					delay = 1000;
					if (res.event) this.#apply(res.event);
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
		this.tasks.clear();
		this.instances.clear();
		this.installs.clear();
		this.slots.clear();
		this.sources.clear();
	}

	#apply(ev: Event): void {
		const gone = ev.action === EventAction.DELETED;
		const p = ev.payload;
		switch (p.case) {
			case 'task':
				put(this.tasks, ev.id, p.value, gone);
				return;
			case 'instance':
				put(this.instances, ev.id, p.value, gone);
				return;
			case 'install':
				put(this.installs, ev.id, p.value, gone);
				return;
			case 'slot':
				put(this.slots, ev.id, p.value, gone);
				return;
			case 'source':
				put(this.sources, ev.id, p.value, gone);
				return;
			case 'host':
				this.host = gone ? undefined : p.value;
				return;
			case 'store':
				this.store = gone ? undefined : p.value;
				return;
			case 'settings':
				this.settings = gone ? undefined : p.value;
				return;
			case 'model':
				clearTimeout(this.#modelsTimer);
				this.#modelsTimer = setTimeout(() => void this.refreshModels().catch((err) => (this.error = reason(err))), 200);
				return;
			default:
				return;
		}
	}
}

export const live = new Live();
