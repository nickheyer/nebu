import { getContext, setContext, untrack } from 'svelte';

export type ListSlot = 'leading' | 'dot' | 'secondary' | 'instrument' | 'action' | 'menu' | 'caret';

export type ListHead = { primary?: string; secondary?: string; instrument?: string; facts?: string[]; action?: string };

const key = Symbol('nebu-list');

// The row width under which phrase and facts move into the expand
export const compactBelow = 640;

// Tracks which cells the rows of one list use so all share the same columns
export class ListLayout {
	#counts = $state<Record<string, number>>({});
	#facts = $state<number[]>([]);
	primaryWeight = $state(1);
	secondaryWeight = $state(1.4);
	width = $state(0);

	readonly compact = $derived(this.width > 0 && this.width < compactBelow);
	readonly factCount = $derived(this.#facts.reduce((m, n) => Math.max(m, n), 0));

	has(slot: ListSlot): boolean {
		if (this.compact && (slot === 'secondary' || slot === 'instrument')) return false;
		return (this.#counts[slot] ?? 0) > 0;
	}

	// Counts a row's cells and returns the release
	use(slots: ListSlot[], facts: number): () => void {
		untrack(() => {
			for (const s of slots) this.#counts[s] = (this.#counts[s] ?? 0) + 1;
			this.#facts.push(facts);
		});
		return () =>
			untrack(() => {
				for (const s of slots) this.#counts[s] = (this.#counts[s] ?? 1) - 1;
				const i = this.#facts.indexOf(facts);
				if (i >= 0) this.#facts.splice(i, 1);
			});
	}

	// The shared column tracks
	readonly template = $derived.by(() => {
		const cols: string[] = [];
		if (this.has('leading')) cols.push('max-content');
		if (this.has('dot')) cols.push('max-content');
		cols.push(`minmax(10ch, ${this.primaryWeight}fr)`);
		if (this.has('secondary')) cols.push(`minmax(0, ${this.secondaryWeight}fr)`);
		if (this.has('instrument')) cols.push('minmax(8rem, 1.6fr)');
		if (!this.compact) for (let i = 0; i < this.factCount; i++) cols.push('max-content');
		if (this.has('action')) cols.push('max-content');
		if (this.has('menu')) cols.push('max-content');
		if (this.has('caret')) cols.push('max-content');
		return cols.join(' ');
	});
}

export function provideList(): ListLayout {
	return setContext(key, new ListLayout());
}

// Starts a scope without a list for rows nested in an expand
export function leaveList() {
	setContext(key, undefined);
}

export function useList(): ListLayout | undefined {
	return getContext<ListLayout | undefined>(key);
}

// A width in ch as fr weight against a 24ch name
export function weightOf(width: string, fallback: number): number {
	return width.endsWith('ch') ? Math.max(0.5, Number(width.slice(0, -2)) / 24) : fallback;
}
