import { SvelteMap } from 'svelte/reactivity';
import { reason, rpc } from '#lib/api/transport.js';
import { FitVerdict, type FitRow } from '#lib/proto/nebu/v1/estimate_pb.js';
import { ModelKind, ModelKindSchema, type Descriptor } from '#lib/proto/nebu/v1/model_pb.js';
import { enumLabel } from '#lib/format.js';
import type { Lamp } from '#lib/ui/status.js';
import { contextLabel, paramLabel } from '#lib/pages/models/models.js';

// A weight group of a repository at a revision
export type ContextRef = { sourceId: string; repo: string; revision: string; group: string };
// Where the weights fit in words, the largest context they reach there, and the whole of it as a sentence, a provider's
// model fitting nowhere here since it runs at its provider
export type ContextFit = { lamp: Lamp; where: string; context: string; tip: string; remote?: boolean };
// What is known of one weight group, nothing while its plan is on the way
export type ContextState = { fit?: ContextFit; what: string; error?: string; warnings: string[] };

const tones: { lamp: Lamp; where: string; label: string }[] = [
	{ lamp: 'success', where: 'Device', label: 'Fits in device memory' },
	{ lamp: 'warning', where: 'Device + system', label: 'Fits in device and system memory together' },
	{ lamp: 'error', where: 'No fit', label: 'Fits in no memory this host has' }
];

function rank(v: FitVerdict | undefined): number {
	return v === FitVerdict.FITS ? 3 : v === FitVerdict.PARTIAL ? 2 : v === FitVerdict.NO ? 1 : 0;
}

// The largest context one weight group reaches on this host over every runtime, by the memory it takes
export function contextFit(rows: FitRow[]): ContextFit | undefined {
	const served = rows.find((r) => r.plan?.remote);
	if (served) {
		const context = served.context > 0 ? contextLabel(served.context) : '';
		return { lamp: 'success', where: 'Provider', context, tip: ['Runs at its provider, nothing loads on this host', context ? `${context} context` : ''].filter(Boolean).join(', '), remote: true };
	}
	const plans = rows.filter((r) => r.plan).map((r) => ({ context: r.context || Number(r.plan?.params['n_ctx'] ?? 0), verdict: r.plan?.verdict }));
	if (plans.length === 0) return undefined;
	const top = Math.max(...plans.map((p) => rank(p.verdict)));
	const best = Math.max(...plans.filter((p) => rank(p.verdict) === top).map((p) => p.context));
	const tone = top === 3 ? tones[0]! : top === 2 ? tones[1]! : tones[2]!;
	if (top < 2) return { lamp: tone.lamp, where: tone.where, context: '', tip: tone.label };
	const context = best > 0 ? contextLabel(best) : '';
	return { lamp: tone.lamp, where: tone.where, context, tip: [tone.label, context ? `at ${context} context` : ''].filter(Boolean).join(' ') };
}

// What a weight group is in a few words, its architecture, size and precision, and its kind when it is no language model
export function describe(d: Descriptor | undefined): string {
	if (!d) return '';
	const kind = d.kind === ModelKind.UNSPECIFIED || d.kind === ModelKind.LANGUAGE ? '' : enumLabel(ModelKindSchema, d.kind);
	return [d.architecture, paramLabel(d.parameterCount), d.bitsPerWeight > 0 ? `${d.bitsPerWeight.toFixed(1)} bits per weight` : '', kind].filter(Boolean).join(', ');
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
				.then((res) => this.#states.set(key, { fit: contextFit(res.rows.filter((r) => r.group === ref.group)), what: describe(res.descriptors.find((d) => d.group === ref.group)), warnings: res.warnings }))
				.catch((err) => this.#states.set(key, { what: '', error: reason(err), warnings: [] }))
				.finally(() => {
					this.#active--;
					this.#next();
				});
		}
	}
}
