<script lang="ts">
	import { rpc } from '#lib/api/transport.js';
	import { bytes } from '#lib/format.js';
	import type { ParamState } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { ParamList, ParamType, type Param } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { ModelSort } from '#lib/proto/nebu/v1/store_pb.js';
	import FileList, { type FileListShape } from '#lib/ui/fields/FileList.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Part from '#lib/ui/fields/Part.svelte';
	import Path from '#lib/ui/fields/Path.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Slider from '#lib/ui/fields/Slider.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import type { PickerPage } from '#lib/ui/fields/types.js';
	import { modelPhrase, partLabel, sliderSpan, storeRef } from '#lib/pages/serve/serve.js';

	// One runtime parameter as the control its type asks for, an empty control leaving the parameter unset
	let { param, values = $bindable({}), supplied, state }: { param: Param; values?: Record<string, string>; supplied?: string; state?: ParamState } = $props();

	const p = $derived(param);
	const label = $derived(p.label || p.name);
	const info = $derived([p.description, p.rule ? `empty takes ${p.rule}` : ''].filter(Boolean).join('\n') || undefined);
	// What an empty control shows, the plan's value, else the runtime's default, else what the runtime keeps unset
	const idle = $derived(supplied || p.default || p.placeholder || undefined);
	const idleNumber = $derived(idle !== undefined && Number.isFinite(Number(idle)) ? Number(idle) : undefined);
	const span = $derived(sliderSpan(p, state));
	const step = $derived(p.step || (p.type === ParamType.FLOAT ? 0.01 : 1));
	const numeric = $derived(p.type === ParamType.INT || p.type === ParamType.FLOAT);
	const current = $derived(values[p.name] ?? '');
	// The number a box shows, none for a value that is no number
	const number = $derived(current !== '' && Number.isFinite(Number(current)) ? Number(current) : undefined);
	// What a switch shows while unset, the state the runtime keeps by itself
	const resting = $derived(p.default === 'true');

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

	// A switch back at the state the runtime keeps by itself leaves the parameter unset
	function flip(on: boolean) {
		set(on === resting ? undefined : String(on));
	}

	// The files of a list and of the scaled list paired with it, each going to its own parameter
	function list(files: string, scaled: string) {
		set(files);
		if (p.scaledIn) put(p.scaledIn, scaled);
	}
</script>

<div class="param">
	{#if p.picks && p.type === ParamType.PATH}
		<Part value={current} fetch={fetchParts} {label} {info} placeholder={idle && partLabel(idle)} current={current ? partLabel(current) : undefined} filter={p.directory ? 'directories' : 'any'} onchange={set} />
	{:else if p.picks}
		<Picker value={current || undefined} fetch={fetchNamed} {label} {info} placeholder={idle} current={current || undefined} clearable onchange={set} />
	{:else if shapes[p.list]}
		<FileList value={current} scaled={p.scaledIn ? (values[p.scaledIn] ?? '') : undefined} shape={shapes[p.list]} {label} {info} onchange={list} />
	{:else if p.choices.length}
		<Select value={current || undefined} options={p.choices.filter((c) => c !== '').map((c) => ({ value: c, label: c }))} {label} {info} placeholder={idle} clearable onchange={set} />
	{:else if p.type === ParamType.BOOL}
		<Switch checked={current === '' ? resting : current === 'true'} {label} {info} stacked onchange={flip} />
	{:else if p.type === ParamType.PATH}
		<Path value={current} {label} {info} placeholder={idle && partLabel(idle)} filter={p.directory ? 'directories' : 'any'} onchange={set} />
	{:else if span}
		<Slider value={number} min={span.min} max={span.max} step={span.step} curve={span.curve} unit={p.unit || undefined} {label} {info} placeholder={idle} optional onchange={set} />
	{:else if numeric}
		<NumberField value={number} {step} unit={p.unit || undefined} {label} {info} placeholder={idle} fallback={idleNumber} fraction={p.type === ParamType.FLOAT} optional plain onchange={set} onclear={() => set(undefined)} />
	{:else}
		<Text value={current} {label} {info} placeholder={idle} onchange={set} />
	{/if}
</div>

<style>
	.param {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
	}
</style>
