import { SvelteMap } from 'svelte/reactivity';
import { reason, rpc } from '#lib/api/transport.js';
import { FitVerdict, type FitRow } from '#lib/proto/nebu/v1/estimate_pb.js';
import type { Lamp } from '#lib/ui/status.js';
import { contextLabel } from '#lib/pages/models/models.js';

export type ContextRef = { sourceId: string; repo: string; revision: string; group: string };
export type ContextFit = { lamp: Lamp; label: string; tip: string };
// What is known of one weight group, nothing while its plan is on the way
export type ContextState = { fit?: ContextFit; error?: string; warnings: string[] };

export const contextTones: { lamp: Lamp; label: string }[] = [
	{ lamp: 'success', label: 'Fits in device memory' },
	{ lamp: 'warning', label: 'Fits in system + device memory' },
	{ lamp: 'error', label: 'No fit' }
];

function rank(v: FitVerdict | undefined): number {
	return v === FitVerdict.FITS ? 3 : v === FitVerdict.PARTIAL ? 2 : v === FitVerdict.NO ? 1 : 0;
}

// The largest context a weight group reaches on this host over every runtime, lit by the memory it takes
export function contextFit(rows: FitRow[]): ContextFit | undefined {
	const plans = rows.filter((r) => r.plan).map((r) => ({ context: r.context || Number(r.plan?.params['n_ctx'] ?? 0), verdict: r.plan?.verdict }));
	if (plans.length === 0) return undefined;
	const top = Math.max(...plans.map((p) => rank(p.verdict)));
	const best = Math.max(...plans.filter((p) => rank(p.verdict) === top).map((p) => p.context));
	const tone = top === 3 ? contextTones[0]! : top === 2 ? contextTones[1]! : contextTones[2]!;
	if (top < 2) return { lamp: tone.lamp, label: 'No fit', tip: tone.label };
	return best > 0 ? { lamp: tone.lamp, label: contextLabel(best), tip: `${tone.label} at ${contextLabel(best)} context` } : { lamp: tone.lamp, label: 'Fits', tip: tone.label };
}

function keyOf(r: ContextRef): string {
	return [r.sourceId, r.repo, r.revision, r.group].join('\n');
}

// Plans weight groups as their rows ask, three at a time in the order asked, each planned once
export class Contexts {
	#states = new SvelteMap<string, ContextState>();
	#queue: ContextRef[] = [];
	#asked = new Set<string>();
	#active = 0;

	get(ref: ContextRef): ContextState | undefined {
		return this.#states.get(keyOf(ref));
	}

	want(ref: ContextRef): void {
		const key = keyOf(ref);
		if (this.#asked.has(key)) return;
		this.#asked.add(key);
		this.#queue.push(ref);
		this.#next();
	}

	#next(): void {
		while (this.#active < 3 && this.#queue.length) {
			const ref = this.#queue.shift()!;
			const key = keyOf(ref);
			this.#active++;
			rpc.estimate
				.inspect({ sourceId: ref.sourceId, repo: ref.repo, revision: ref.revision, groups: [ref.group] })
				.then((res) => this.#states.set(key, { fit: contextFit(res.rows.filter((r) => r.group === ref.group)), warnings: res.warnings }))
				.catch((err) => this.#states.set(key, { error: reason(err), warnings: [] }))
				.finally(() => {
					this.#active--;
					this.#next();
				});
		}
	}
}
