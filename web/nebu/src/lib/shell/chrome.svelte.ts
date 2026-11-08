import type { Snippet } from 'svelte';

export type HeaderTab = { id: string; label: string; count?: number; href?: string };

export type HeaderSpec = {
	title?: string;
	// A link one level up shown before the title, a record page's way back to its list
	back?: { label: string; href: string };
	// Pills and short facts after the title
	meta?: Snippet;
	// The page's buttons, the primary one last
	actions?: Snippet;
	tabs?: HeaderTab[];
	active?: string;
	ontab?: (id: string) => void;
};

let current = $state<{ id: number; spec: HeaderSpec } | undefined>(undefined);
let seq = 0;

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
	// Clears only the header this id set, so a leaving page never wipes the next page's header
	clear(id: number) {
		if (current?.id === id) current = undefined;
	}
};
