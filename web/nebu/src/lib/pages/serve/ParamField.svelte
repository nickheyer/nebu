<script lang="ts">
	import { rpc } from '#lib/api/transport.js';
	import { bytes, capitalize, num } from '#lib/format.js';
	import type { ParamState } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { ParamList, ParamType, type Param } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { ModelSort } from '#lib/proto/nebu/v1/store_pb.js';
	import FileList, { type FileListShape } from '#lib/ui/fields/FileList.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Part from '#lib/ui/fields/Part.svelte';
	import Path from '#lib/ui/fields/Path.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import Resting from '#lib/ui/fields/Resting.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Slider from '#lib/ui/fields/Slider.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import type { Named } from '#lib/ui/fields/scale.js';
	import type { PickerPage } from '#lib/ui/fields/types.js';
	import { modelPhrase, partLabel, sliderSpan, storeRef } from '#lib/pages/serve/serve.js';

	// One runtime parameter as the control its type asks for, empty while unset with what it rests on written under it
	let { param, values = $bindable({}), supplied, state, restingWord = 'Default' }: { param: Param; values?: Record<string, string>; supplied?: string; state?: ParamState; restingWord?: string } = $props();

	const p = $derived(param);
	const label = $derived(p.label || p.name);
	const info = $derived([p.description, p.rule ? `Unset takes ${p.rule}` : ''].filter(Boolean).join('\n') || undefined);
	// What the runtime holds by itself, and what the param rests on once a plan has worked a value out
	const own = $derived(p.default || p.placeholder || undefined);
	const planned = $derived(supplied && supplied !== 'auto' ? supplied : undefined);
	const idle = $derived(planned ?? own);
	const numeric = $derived(p.type === ParamType.INT || p.type === ParamType.FLOAT);
	const names = $derived<Named[]>(numeric ? p.names.map((n) => ({ value: Number(n.value), label: n.label })) : []);
	const idleNumber = $derived(numeric && idle !== undefined && Number.isFinite(Number(idle)) ? Number(idle) : undefined);
	const span = $derived(sliderSpan(p, state));
	const step = $derived(p.step || (p.type === ParamType.FLOAT ? 0.01 : 1));
	const current = $derived(values[p.name] ?? '');
	// The number a box shows, none for a value that is no number
	const number = $derived(current !== '' && Number.isFinite(Number(current)) ? Number(current) : undefined);

	// A param of on, off and auto alone, a switch once it rests on on or off and three buttons while it rests on neither
	const states = $derived(p.choices.filter((c) => c !== ''));
	const toggles = $derived(p.type === ParamType.BOOL || (states.length > 0 && states.every((c) => c === 'on' || c === 'off' || c === 'auto')));
	const yes = $derived(p.type === ParamType.BOOL ? 'true' : 'on');
	const no = $derived(p.type === ParamType.BOOL ? 'false' : 'off');
	const binary = $derived(toggles && !states.includes('auto') && (idle === yes || idle === no || p.type === ParamType.BOOL));
	const restsOn = $derived(p.type === ParamType.BOOL ? (idle === 'true' ? 'true' : 'false') : idle);
	// The states three buttons offer, the unset one first under the word for what it comes to when no state says it
	const ways = $derived([...(restsOn !== undefined && states.includes(restsOn) ? [] : [{ value: '', label: capitalize(restsOn ?? 'unset') }]), ...states.map((c) => ({ value: c, label: capitalize(c) }))]);

	// A value in words, a named number by its name, a number with its unit, a stored part by its model
	function words(v: string): string {
		if (p.type === ParamType.BOOL) return v === 'true' ? 'on' : 'off';
		if (numeric && Number.isFinite(Number(v))) return names.find((n) => n.value === Number(v))?.label ?? [Number.isInteger(Number(v)) ? num(Number(v)) : v, p.unit].filter(Boolean).join(' ');
		return p.type === ParamType.PATH ? partLabel(v) : v;
	}
	// What the param rests on while unset, the runtime's own word first when a plan has put a value to it
	const resting = $derived(idle === undefined ? '' : planned !== undefined && own !== undefined && own !== planned && !Number.isFinite(Number(own)) ? `${words(own)}, ${words(planned)}` : words(idle));
	// Named numbers a box offers as buttons, all but the one in it and the one the param rests on
	const offers = $derived(names.filter((n) => n.value !== number && n.value !== idleNumber));

	const shapes: Partial<Record<ParamList, FileListShape>> = { [ParamList.FILES]: 'files', [ParamList.SCALED]: 'scaled', [ParamList.NAMED]: 'named' };

	// Every stored group, each as the reference a launch resolves to the file the param loads
	async function fetchParts(query: string, cursor: string, limit: number, signal: AbortSignal): Promise<PickerPage<string>> {
		const res = await rpc.store.listModels({ query, cursor, limit, sort: ModelSort.LAST_USED }, { signal });
		return { items: res.models.map((m) => ({ value: storeRef(m), label: modelPhrase(m), description: bytes(m.bytes) })), next: res.nextCursor };
	}

	// The param's own choices holding the typed words, then every stored group by its group name
	async function fetchNamed(query: string, cursor: string, limit: number, signal: AbortSignal): Promise<PickerPage<string>> {
		const res = await rpc.store.listModels({ query, cursor, limit, sort: ModelSort.LAST_USED }, { signal });
		const words = query.toLowerCase().split(/\s+/).filter(Boolean);
		const own = cursor ? [] : p.choices.filter((c) => c !== '' && words.every((w) => c.toLowerCase().includes(w))).map((c) => ({ value: c, label: c }));
		return { items: [...own, ...res.models.map((m) => ({ value: m.group, label: m.group, description: m.repo }))], next: res.nextCursor };
	}

	function put(name: string, v: string | number | undefined) {
		if (v === undefined || v === '') delete values[name];
		else values[name] = String(v);
	}

	function set(v: string | number | undefined) {
		put(p.name, v);
	}

	// A state back at the one the param rests on leaves the parameter unset
	function state_(v: string) {
		set(v === '' || v === restsOn ? undefined : v);
	}

	// The files of a list and of the scaled list paired with it, each going to its own parameter
	function list(files: string, scaled: string) {
		set(files);
		if (p.scaledIn) put(p.scaledIn, scaled);
	}
</script>

<div class="param">
	{#if p.picks && p.type === ParamType.PATH}
		<Part value={current} fetch={fetchParts} {label} {info} current={current ? partLabel(current) : undefined} filter={p.directory ? 'directories' : 'any'} onchange={set} />
	{:else if p.picks}
		<Picker value={current || undefined} fetch={fetchNamed} {label} {info} current={current || undefined} clearable onchange={set} />
	{:else if shapes[p.list]}
		<FileList value={current} scaled={p.scaledIn ? (values[p.scaledIn] ?? '') : undefined} shape={shapes[p.list]} {label} {info} onchange={list} />
	{:else if binary}
		<Switch checked={(current === '' ? restsOn : current) === yes} {label} {info} stacked onchange={(on) => state_(on ? yes : no)} />
	{:else if toggles}
		<Segmented value={current === '' && restsOn !== undefined && states.includes(restsOn) ? restsOn : current} options={ways} {label} {info} onchange={state_} />
	{:else if p.choices.length}
		<Select value={current || undefined} options={states.map((c) => ({ value: c, label: c }))} {label} {info} clearable onchange={set} />
	{:else if p.type === ParamType.PATH}
		<Path value={current} {label} {info} filter={p.directory ? 'directories' : 'any'} onchange={set} />
	{:else if span}
		<Slider value={number} min={span.min} max={span.max} step={span.step} curve={span.curve} unit={p.unit || undefined} {label} {info} resting={idle} restingText={resting} {restingWord} {names} optional bounded={false} onchange={set} />
	{:else if numeric}
		<NumberField value={number} {step} unit={p.unit || undefined} {label} {info} caption={false} fallback={idleNumber !== undefined && !names.some((n) => n.value === idleNumber) ? idleNumber : undefined} fraction={p.type === ParamType.FLOAT} {names} optional plain onchange={set} onclear={() => set(undefined)} />
	{:else}
		<Text value={current} {label} {info} onchange={set} />
	{/if}
	{#if !shapes[p.list] && !span}
		<Resting word={restingWord} text={resting} offers={offers.map((n) => ({ label: n.label, pick: () => set(n.value) }))} set={current !== ''} about={label} onreset={() => set(undefined)} />
	{/if}
</div>

<style>
	.param {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}
</style>
