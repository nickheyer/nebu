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
