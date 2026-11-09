<script lang="ts" generics="V extends string">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoSelect } from '#lib/neo/inputs/index.js';
	import type { NeoListItem, NeoListContext, NeoListSelectedItem } from '#lib/neo/list/index.js';
	import { isCancel, reason } from '#lib/api/transport.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import { fieldId, listPopover, selectToggle, type Option, type PickerFetch } from '#lib/ui/fields/types.js';

	let {
		value = $bindable(),
		fetch,
		label,
		info,
		placeholder,
		disabled = false,
		clearable = false,
		required = false,
		message,
		error,
		pageSize = 25,
		current,
		// A toolbar picker, its label standing as the placeholder and prefixed to the chosen option
		compact = false,
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
		compact?: boolean;
		onchange?: (v: V | undefined) => void;
	} = $props();

	const id = fieldId();
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

	// The chosen option behind its label in a toolbar, alone in a form
	function prefixed(selection?: NeoListSelectedItem<V> | NeoListSelectedItem<V>[]): string | undefined {
		const one = Array.isArray(selection) ? selection[0] : selection;
		const text = one?.item?.label ?? one?.item?.value?.toString();
		if (text === undefined) return undefined;
		return compact && label ? `${label}: ${text}` : text;
	}

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

<div class="field" class:compact>
	{#if !compact}<FieldLabel {label} {info} forId={id} {required} />{/if}
	<NeoSelect
		bind:value
		bind:open
		bind:highlight
		options={items}
		{id}
		filter={() => true}
		sort={() => 0}
		search
		placeholder={placeholder ?? (compact ? label : 'Select')}
		display={prefixed}
		{disabled}
		{clearable}
		{required}
		nullable={clearable}
		elevation={-1}
		placement="inside"
		floating={false}
		message={error ?? message}
		{error}
		width="100%"
		aria-label={label}
		listProps={{ ...listPopover, after, loading: loading && options.length > 0, searchProps: { sort: false } }}
		buttonProps={selectToggle}
		onChange={(v) => onchange?.(v as V | undefined)}
	/>
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.field :global(.neo-input-group) {
		min-width: 0;
	}
	.compact :global(.neo-input-group) {
		margin-top: 0;
	}
	.compact :global(.neo-input-display-content) {
		overflow: hidden;
		white-space: nowrap;
		text-overflow: ellipsis;
	}
	.note {
		display: flex;
		justify-content: center;
		padding: 6px;
		font-size: 13px;
	}
</style>
