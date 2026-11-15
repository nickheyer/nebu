import { untrack } from 'svelte';

export type HeaderTab = { id: string; label: string; href?: string };

export type HeaderSpec = {
	title?: string;
	back?: { label: string; href: string };
	// Where the titled thing comes from, the title then linking out to it
	origin?: { label: string; href: string };
	tabs?: HeaderTab[];
	active?: string;
	ontab?: (id: string) => void;
};

let current = $state<{ id: number; spec: HeaderSpec } | undefined>(undefined);
let seq = 0;
// Pages asking for the whole window, the shell's rail and bars standing down while any does
let immersed = $state(0);

// The header the mounted page declared, rendered by the top bar
export const chrome = {
	get header(): HeaderSpec | undefined {
		return current?.spec;
	},
	set(spec: HeaderSpec): number {
		const id = ++seq;
		current = { id, spec };
		return id;
	},
	// Clears only the header this id set
	clear(id: number) {
		if (current?.id === id) current = undefined;
	}
};

// Whether a page has taken the whole window, and the way a page takes it and gives it back
export const immersion = {
	get on(): boolean {
		return immersed > 0;
	},
	// Counts untracked, so an effect that enters does not come to depend on the count it changes
	enter(): () => void {
		untrack(() => immersed++);
		let held = true;
		return () => {
			if (held) untrack(() => immersed--);
			held = false;
		};
	}
};
