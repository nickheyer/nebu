<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import DirectoryBrowser, { type BrowseFilter } from '#lib/ui/fields/DirectoryBrowser.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import type { PickerFetch } from '#lib/ui/fields/types.js';
	import FolderOpenIcon from 'phosphor-svelte/lib/FolderOpenIcon';

	// A stored part picked from the daemon, or a file outside the store browsed to
	let {
		value = $bindable(''),
		fetch,
		label,
		info,
		placeholder,
		message,
		current,
		filter = 'any',
		disabled = false,
		onchange
	}: {
		value?: string;
		fetch: PickerFetch<string>;
		label?: string;
		info?: string;
		placeholder?: string;
		message?: string;
		current?: string;
		filter?: BrowseFilter;
		disabled?: boolean;
		onchange?: (v: string) => void;
	} = $props();

	let open = $state(false);
	// The directory browser starts at a browsed file, a stored part having no place on disk to start from
	const start = $derived(value.includes('://') ? '' : value);

	function set(v: string | undefined) {
		value = v ?? '';
		onchange?.(value);
	}
</script>

{#snippet browse()}
	<NeoButton text {disabled} onclick={(e: MouseEvent) => (e.stopPropagation(), (open = true))} aria-label="Browse" title="Browse">
		{#snippet icon()}<FolderOpenIcon size={18} />{/snippet}
	</NeoButton>
{/snippet}

<div class="part">
	<Picker value={value || undefined} {fetch} {label} {info} {placeholder} {message} {disabled} {current} clearable trailing={browse} onchange={set} />
</div>
<DirectoryBrowser bind:open {start} {filter} onpick={set} />

<style>
	/* The browse button fits inside a box as tall as a select beside it */
	.part {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.part :global(.neo-input-after) {
		padding-top: 0;
		padding-bottom: 0;
	}
	.part :global(.neo-input-after .neo-button) {
		--neo-btn-min-width: 2rem;
		--neo-btn-padding-empty: 0.375rem;
	}
</style>
