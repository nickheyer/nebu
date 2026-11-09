import { getContext, setContext } from 'svelte';

// The strip under a sheet's scrolling form that holds its commit row
export type SheetFoot = { readonly el: HTMLElement | undefined };

const key = Symbol('nebu-sheet-foot');

export function provideSheetFoot(foot: SheetFoot): void {
	setContext(key, foot);
}

export function useSheetFoot(): SheetFoot | undefined {
	return getContext<SheetFoot | undefined>(key);
}
