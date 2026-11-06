<script lang="ts" generics="V extends string">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoSelect } from '#lib/neo/inputs/index.js';
	import type { NeoListItem, NeoListContext } from '#lib/neo/list/index.js';
	import { isCancel, reason } from '#lib/api/transport.js';
	import type { Option, PickerFetch } from '#lib/ui/fields/types.js';

	let {
		value = $bindable(),
		fetch,
		label,
		info,
		placeholder = 'Search',
		disabled = false,
		clearable = false,
		required = false,
		message,
		error,
		pageSize = 25,
		// The label of the current value when it is not among the loaded options
		current,
		width = '100%',
		onchange
	}: {
		value?: V;
		fetch: PickerFetch<V>;
		label?: string;
		info?: string;
		placeholder?: string;
		disabled?: boolean;
		clearable?: boolean;
		required?: boolean;
		message?: string;
		error?: string;
		pageSize?: number;
		current?: string;
		width?: string;
		onchange?: (v: V | undefined) => void;
	} = $props();

	let highlight = $state<string | undefined>('');
	let options = $state<Option<V>[]>([]);
	let next = $state('');
	let loading = $state(false);
	let failure = $state<string | null>(null);
	let open = $state(false);
	let abort: AbortController | undefined;
	let timer: ReturnType<typeof setTimeout> | undefined;

	const items = $derived.by((): NeoListItem<V>[] => {
		const list = options.map((o): NeoListItem<V> => ({ id: o.value, value: o.value, label: o.label, description: o.description, disabled: o.disabled }));
		if (value !== undefined && !list.some((i) => i.value === value)) list.unshift({ id: value, value, label: current ?? value });
		return list;
	});

	async function run(query: string, cursor: string, append: boolean) {
		abort?.abort();
		const ac = new AbortController();
		abort = ac;
		loading = true;
		try {
			const page = await fetch(query, cursor, pageSize, ac.signal);
			if (ac.signal.aborted) return;
			options = append ? [...options, ...page.items] : page.items;
			next = page.next;
			failure = null;
		} catch (err) {
			if (isCancel(err) || ac.signal.aborted) return;
			failure = reason(err);
		} finally {
			if (!ac.signal.aborted) loading = false;
		}
	}

	$effect(() => {
		const q = highlight ?? '';
		if (!open) return;
		clearTimeout(timer);
		timer = setTimeout(() => void run(q, '', false), 250);
	});

	function more() {
		void run(highlight ?? '', next, true);
	}
</script>

{#snippet after(_: NeoListContext)}
	{#if failure}
		<div class="note error-text">{failure}</div>
	{:else if next}
		<div class="note"><NeoButton text {loading} onclick={more}>More</NeoButton></div>
	{:else if loading && options.length === 0}
		<div class="note secondary">Searching</div>
	{/if}
{/snippet}

<NeoSelect
	bind:value
	bind:open
	bind:highlight
	options={items}
	filter={() => true}
	sort={() => 0}
	search
	{label}
	{placeholder}
	{disabled}
	{clearable}
	{required}
	nullable={clearable}
	elevation={-1}
	placement={label ? 'top' : 'inside'}
	floating={false}
	message={error ?? (info ? `${message ? `${message}. ` : ''}${info}` : message)}
	{error}
	{width}
	listProps={{ after, loading: loading && options.length > 0, searchProps: { placeholder, sort: false } }}
	onChange={(v) => onchange?.(v as V | undefined)}
/>

<style>
	.note {
		display: flex;
		justify-content: center;
		padding: 6px;
		font-size: 13px;
	}
</style>
