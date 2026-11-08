import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { isCancel, reason, rpc } from '#lib/api/transport.js';
import { live } from '#lib/live/events.svelte.js';
import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
import type { GatewayStatus, Trace } from '#lib/proto/nebu/v1/gateway_pb.js';
import type { PoolImport } from '#lib/proto/nebu/v1/poolimport_pb.js';
import { toast } from '#lib/shell/notify.svelte.js';
import { prefs } from '#lib/state/prefs.svelte.js';
import { clockTick } from '#lib/ui/clock.svelte.js';
import { landingMs, started } from '#lib/pages/traffic/traffic.js';

// The gateway status, imported pools and recent traffic every Gateway tab reads, followed while a tab is open
export class GatewayGroup {
	status = $state<GatewayStatus | undefined>(undefined);
	imports = $state<PoolImport[]>([]);
	importsLoaded = $state(false);
	#seed = $state<Trace[]>([]);
	#seeded = $state(false);
	#span = landingMs(prefs.trafficWindow);

	// Requests the Traffic tab lists when opened, the seed and the stream counted once each across the remembered window
	readonly traffic = $derived.by(() => {
		if (!this.#seeded) return undefined;
		const from = clockTick.now - this.#span;
		const ids = new Set<string>();
		for (const t of this.#seed) if (started(t) >= from) ids.add(t.id);
		for (const t of live.traces) if (started(t) >= from) ids.add(t.id);
		return ids.size;
	});

	// Loads and follows everything, the traffic seed only where the page does not count its own requests
	start(withTraffic: boolean): () => void {
		const abort = new AbortController();
		void this.#loadStatus();
		void this.#loadImports();
		void this.#watchImports(abort.signal);
		if (withTraffic) void this.#loadTraffic(abort.signal);
		let timer: ReturnType<typeof setTimeout> | undefined;
		const off = live.on(EventKind.ROUTE, () => {
			clearTimeout(timer);
			timer = setTimeout(() => void this.#loadStatus(), 2000);
		});
		return () => {
			abort.abort();
			clearTimeout(timer);
			off();
		};
	}

	async #loadStatus() {
		try {
			this.status = (await rpc.gateway.getGatewayStatus({})).status;
		} catch (err) {
			toast.error('Gateway status failed to load', reason(err));
		}
	}

	async #loadImports() {
		try {
			this.imports = (await rpc.imports.listPoolImports({})).imports.sort((a, b) => a.id.localeCompare(b.id));
		} catch (err) {
			toast.error('Imported pools failed to load', reason(err));
		} finally {
			this.importsLoaded = true;
		}
	}

	// Follows imported pools, reconnecting with backoff and toasting once per run of failures
	async #watchImports(signal: AbortSignal) {
		let delay = 1000;
		let warned = false;
		while (!signal.aborted) {
			try {
				for await (const w of rpc.imports.watchPoolImports({}, { signal })) {
					delay = 1000;
					warned = false;
					const imp = w.import;
					if (!imp) continue;
					const rest = this.imports.filter((x) => x.id !== imp.id);
					this.imports = w.deleted ? rest : [...rest, imp].sort((a, b) => a.id.localeCompare(b.id));
				}
			} catch (err) {
				if (isCancel(err) || signal.aborted) return;
				if (!warned) toast.error('Imported pools stopped updating', reason(err));
				warned = true;
			}
			await new Promise((resolve) => setTimeout(resolve, delay));
			delay = Math.min(delay * 2, 30_000);
			if (!signal.aborted) await this.#loadImports();
		}
	}

	// Every trace of the remembered window without bodies, the stream adding the ones that start later
	async #loadTraffic(signal: AbortSignal) {
		try {
			const res = await rpc.gateway.listTraces({ from: timestampFromDate(new Date(Date.now() - this.#span)), limit: 0 }, { signal });
			this.#seed = res.traces;
			this.#seeded = true;
		} catch (err) {
			if (!isCancel(err)) toast.error('Recent requests failed to load', reason(err));
		}
	}
}
