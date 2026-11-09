import ArrowCounterClockwiseIcon from 'phosphor-svelte/lib/ArrowCounterClockwiseIcon';

// One choice a field offers
export type Option<V = string> = { value: V; label: string; description?: string; disabled?: boolean; tip?: string };

export type PickerPage<V> = { items: Option<V>[]; next: string };
// Asks the daemon for the options matching typed text, one page at a time
export type PickerFetch<V> = (query: string, cursor: string, limit: number, signal: AbortSignal) => Promise<PickerPage<V>>;

// The toggle that empties a field and what an empty field then means
export type Off = { label: string };

// The one glyph every off toggle wears, the words of what empty means in its tooltip and placeholder
export const OffIcon = ArrowCounterClockwiseIcon;

// The chevron of a select, a quiet text button rather than a second pressed box inside the first
export const selectToggle = { text: true, elevation: 0, hover: 0, active: 0, pressed: false, borderless: true } as const;

// Option lists mount at the document root, stay on the vertical axis and scroll inside the space left under or over their box
export const listPopover = { height: { max: 'available' }, tooltipProps: { portal: true, flipOptions: { fallbackAxisSideDirection: 'none' } } } as const;

let seq = 0;

// A unique id tying a field's label to its control
export function fieldId(): string {
	return `field-${++seq}`;
}
