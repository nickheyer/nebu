import { onDestroy } from 'svelte';
import { isCancel, reason } from '#lib/api/transport.js';
import { live } from '#lib/live/events.svelte.js';
import type { EventKind } from '#lib/proto/nebu/v1/event_pb.js';

export type Page<T> = { items: T[]; next: string };
export type Fetch<T, P> = (params: P, cursor: string, limit: number, signal: AbortSignal) => Promise<Page<T>>;

// A daemon queried list that pages by cursor and refetches its loaded rows when the stream reports its kinds
export class ListQuery<T, P> {
	items = $state<T[]>([]);
	next = $state('');
	loading = $state(false);
	error = $state<string | null>(null);
	// Set after the first answer so skeletons give way to rows or the empty state
	settled = $state(false);
	#params: P | undefined;
	#abort: AbortController | undefined;
	#timer: ReturnType<typeof setTimeout> | undefined;
	#stops: (() => void)[] = [];

	constructor(
		private fetch: Fetch<T, P>,
		kinds: EventKind[],
		private pageSize = 50
	) {
		for (const kind of kinds) this.#stops.push(live.on(kind, () => this.refresh()));
		onDestroy(() => this.dispose());
	}

	// Loads the first page for new parameters
	async load(params: P): Promise<void> {
		this.#params = params;
		await this.#run(params, '', this.pageSize, false);
	}

	async more(): Promise<void> {
		if (!this.#params || !this.next) return;
		await this.#run(this.#params, this.next, this.pageSize, true);
	}

	// Refetches every loaded row after a short quiet period
	refresh(): void {
		clearTimeout(this.#timer);
		this.#timer = setTimeout(() => {
			if (this.#params === undefined) return;
			void this.#run(this.#params, '', Math.max(this.items.length, this.pageSize), false);
		}, 300);
	}

	dispose(): void {
		clearTimeout(this.#timer);
		this.#abort?.abort();
		this.#stops.forEach((stop) => stop());
	}

	async #run(params: P, cursor: string, limit: number, append: boolean): Promise<void> {
		this.#abort?.abort();
		const abort = new AbortController();
		this.#abort = abort;
		this.loading = true;
		try {
			const page = await this.fetch(params, cursor, limit, abort.signal);
			if (abort.signal.aborted) return;
			this.items = append ? [...this.items, ...page.items] : page.items;
			this.next = page.next;
			this.error = null;
		} catch (err) {
			if (isCancel(err) || abort.signal.aborted) return;
			this.error = reason(err);
		} finally {
			if (!abort.signal.aborted) {
				this.loading = false;
				this.settled = true;
			}
		}
	}
}
