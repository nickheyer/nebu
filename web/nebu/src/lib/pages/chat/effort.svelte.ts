// How hard a chat asks its model to reason: the levels the model itself takes, the one its provider lists as its default, and the one the person last chose for it
import type { ModelEntry } from '#lib/api/gateway.js';
import { capitalize } from '#lib/format.js';
import { prefs } from '#lib/state/prefs.svelte.js';
import type { Option } from '#lib/ui/fields/types.js';

// The levels in rising order, one a model lists that is none of these keeping its own place
const ladder = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];
// A level as the providers' own interfaces word it, capitalized where they word it no other way
const names: Record<string, string> = { xhigh: 'Extra high' };

function effortLabel(level: string): string {
	return names[level] ?? capitalize(level);
}

// The levels a model takes, lowest first, none for a model that takes none
export function effortLevels(model: ModelEntry | undefined): string[] {
	return [...(model?.reasoning?.supported_efforts ?? [])].reverse();
}

// The level the model's provider lists as its default, empty when it lists none
export function effortDefault(model: ModelEntry | undefined): string {
	const level = model?.reasoning?.default_effort ?? '';
	return effortLevels(model).includes(level) ? level : '';
}

// The level a request to the model asks for: the one the person last chose for it, or the highest the model takes below that once it no longer takes it, else the model's default. Empty when the model takes no level, or lists no default and the person chose none, the request then naming none
export function effortOf(model: ModelEntry | undefined): string {
	const levels = effortLevels(model);
	if (!model || levels.length === 0) return '';
	const chosen = prefs.efforts[model.id] ?? '';
	if (levels.includes(chosen)) return chosen;
	const at = ladder.indexOf(chosen);
	const below = at < 0 ? undefined : levels.findLast((l) => ladder.indexOf(l) >= 0 && ladder.indexOf(l) <= at);
	return below ?? effortDefault(model);
}

// Keeps the level the person chose for a model, forgetting it when it is the model's default or none, so the model's default is followed again
export function chooseEffort(model: ModelEntry, level: string | undefined): void {
	const { [model.id]: _, ...rest } = prefs.efforts;
	prefs.efforts = level && level !== effortDefault(model) ? { ...rest, [model.id]: level } : rest;
}

// The levels of a model as the choices of a menu, each with its provider's word on it and the default marked as the providers' own menus mark it
export function effortOptions(model: ModelEntry | undefined): Option<string>[] {
	const standard = effortDefault(model);
	return effortLevels(model).map((level) => ({ value: level, label: effortLabel(level), description: [level === standard ? 'Default' : '', model?.reasoning?.descriptions?.[level] ?? ''].filter(Boolean).join('. ') || undefined }));
}
