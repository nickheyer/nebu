// One choice a field offers
export type Option<V = string> = { value: V; label: string; description?: string; disabled?: boolean; tip?: string };

export type PickerPage<V> = { items: Option<V>[]; next: string };
// Asks the daemon for the options matching typed text, one page at a time
export type PickerFetch<V> = (query: string, cursor: string, limit: number, signal: AbortSignal) => Promise<PickerPage<V>>;
