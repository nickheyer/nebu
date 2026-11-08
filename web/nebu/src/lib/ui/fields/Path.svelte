<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoInput } from '#lib/neo/inputs/index.js';
	import DirectoryBrowser, { type BrowseFilter } from '#lib/ui/fields/DirectoryBrowser.svelte';
	import FolderOpenIcon from 'phosphor-svelte/lib/FolderOpenIcon';

	let {
		value = $bindable(''),
		label,
		placeholder,
		filter = 'any',
		disabled = false,
		message,
		error,
		mark,
		onchange
	}: { value?: string; label?: string; placeholder?: string; filter?: BrowseFilter; disabled?: boolean; message?: string; error?: string; mark?: Snippet; onchange?: (v: string) => void } = $props();

	let open = $state(false);

	function picked(p: string) {
		value = p;
		onchange?.(p);
	}
</script>

<div class="field">
	<NeoInput bind:value {label} {placeholder} {disabled} elevation={-1} placement={label ? 'top' : 'inside'} floating={false} width="100%" message={error ?? message} {error} autocomplete="off" class="mono-input" aria-label={label} oninput={() => onchange?.(value)} />
	<span class="trail">
		{#if mark}<span class="mark">{@render mark()}</span>{/if}
		<NeoButton text {disabled} onclick={() => (open = true)} aria-label="Browse">
			{#snippet icon()}<FolderOpenIcon size={20} />{/snippet}
		</NeoButton>
	</span>
</div>
<DirectoryBrowser bind:open start={value} {filter} onpick={picked} />

<style>
	.field {
		display: flex;
		gap: 8px;
		align-items: flex-end;
		min-width: 0;
	}
	.field :global(.neo-input-group) {
		flex: 1 1 auto;
	}
	.trail {
		display: inline-flex;
		gap: 8px;
		align-items: center;
	}
	.mark {
		display: inline-flex;
		color: var(--nebu-text-muted);
	}
	:global(.mono-input input) {
		font-family: var(--nebu-font-mono);
		font-size: 13px;
	}
</style>
