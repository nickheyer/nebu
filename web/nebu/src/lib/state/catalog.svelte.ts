import { equals, fromJson, toJson, type JsonValue } from '@bufbuild/protobuf';
import { reason, rpc } from '#lib/api/transport.js';
import { live } from '#lib/live/events.svelte.js';
import { EventAction, EventKind, type Event } from '#lib/proto/nebu/v1/event_pb.js';
import { ListSourcesResponseSchema, SourceSchema, type SourceStatus } from '#lib/proto/nebu/v1/source_pb.js';

const key = 'nebu.discover.catalog';
// How long an answer stands before a visit asks again
const fresh = 5 * 60_000;

// The answer the browser kept from an earlier visit, nothing when it holds none it can read
function kept(): SourceStatus[] | undefined {
	try {
		const raw = localStorage.getItem(key);
		return raw === null ? undefined : fromJson(ListSourcesResponseSchema, JSON.parse(raw) as JsonValue, { ignoreUnknownFields: true }).sources;
	} catch {
		return undefined;
	}
}

// The configured sources with the sorts, filters and columns each searches by. They change when a source's
// settings do and seldom otherwise, so the last answer is kept between visits and shown at once while a
// stale one is asked for again behind it.
class Catalog {
	sources = $state.raw<SourceStatus[]>([]);
	// Set once there is an answer to show, a kept one included
	loaded = $state(false);
	// Set once the daemon has answered this visit, or failed to
	settled = $state(false);
	// Why the daemon gave no answer, while there is none to show
	error = $state('');
	// Runtime names by the bit each holds in a hit's mask
	runtimeNames = $state.raw(new Map<number, string>());
	#at = 0;
	#asking: Promise<void> | undefined;
	#runtimes: Promise<void> | undefined;

	constructor() {
		const sources = kept();
		if (sources) {
			this.sources = sources;
			this.loaded = true;
		}
		live.on(EventKind.SOURCE, (ev) => {
			if (this.#at && this.#differs(ev)) void this.refresh();
		});
	}

	// Asks the daemon unless the answer held is fresh
	load(): Promise<void> {
		return Date.now() - this.#at < fresh ? Promise.resolve() : this.refresh();
	}

	refresh(): Promise<void> {
		return (this.#asking ??= this.#ask().finally(() => (this.#asking = undefined)));
	}

	// The source of an id, once there is an answer that could name it: a kept answer without it waits on the daemon's own
	async source(id: string): Promise<SourceStatus | undefined> {
		const asked = this.load();
		const find = () => this.sources.find((s) => s.source?.id === id);
		if (!this.loaded || (!find() && !this.settled)) await asked;
		if (this.sources.length === 0 && this.error) throw new Error(this.error);
		return find();
	}

	// Asks for the runtime names once a session
	loadRuntimes(): Promise<void> {
		return (this.#runtimes ??= rpc.runtimes.listRuntimes({}).then(
			(res) => {
				this.runtimeNames = new Map(res.runtimes.map((r) => [r.runtime?.bit ?? 0, r.runtime?.name || r.runtime?.id || '']));
			},
			(err) => {
				this.#runtimes = undefined;
				throw err;
			}
		));
	}

	async #ask(): Promise<void> {
		try {
			const res = await rpc.sources.listSources({});
			this.sources = res.sources;
			this.error = '';
			this.#at = Date.now();
			try {
				localStorage.setItem(key, JSON.stringify(toJson(ListSourcesResponseSchema, res)));
			} catch {
				// A browser that keeps nothing asks again next visit
			}
		} catch (err) {
			if (this.sources.length === 0) this.error = reason(err);
		} finally {
			this.loaded = true;
			this.settled = true;
		}
	}

	// Whether an event tells of a source the held answer does not show as it now is
	#differs(ev: Event): boolean {
		const held = this.sources.find((s) => s.source?.id === ev.id)?.source;
		if (ev.action === EventAction.DELETED) return held !== undefined;
		return ev.payload.case !== 'source' || held === undefined || !equals(SourceSchema, held, ev.payload.value);
	}
}

export const catalog = new Catalog();
