<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoInput } from '#lib/neo/inputs/index.js';
	import InfoTip from '#lib/ui/InfoTip.svelte';
	import DirectoryBrowser, { type BrowseFilter } from '#lib/ui/fields/DirectoryBrowser.svelte';
	import FolderOpenIcon from 'phosphor-svelte/lib/FolderOpenIcon';

	let {
		value = $bindable(''),
		label,
		info,
		placeholder,
		filter = 'any',
		disabled = false,
		message,
		error,
		onchange
	}: { value?: string; label?: string; info?: string; placeholder?: string; filter?: BrowseFilter; disabled?: boolean; message?: string; error?: string; onchange?: (v: string) => void } = $props();

	let open = $state(false);

	function picked(p: string) {
		value = p;
		onchange?.(p);
	}
</script>

<div class="field">
	<NeoInput bind:value {label} {placeholder} {disabled} elevation={-1} placement={label ? 'top' : 'inside'} floating={false} width="100%" message={error ?? message} {error} autocomplete="off" class="mono-input" oninput={() => onchange?.(value)} />
	<span class="trail">
		<NeoButton text {disabled} onclick={() => (open = true)} aria-label="Browse">
			{#snippet icon()}<FolderOpenIcon size={20} />{/snippet}
		</NeoButton>
		{#if info}<InfoTip text={info} />{/if}
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
	:global(.mono-input input) {
		font-family: var(--nebu-font-mono);
		font-size: 13px;
	}
</style>
