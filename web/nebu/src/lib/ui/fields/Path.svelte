<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoInput } from '#lib/neo/inputs/index.js';
	import DirectoryBrowser, { type BrowseFilter } from '#lib/ui/fields/DirectoryBrowser.svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import Resting from '#lib/ui/fields/Resting.svelte';
	import { fieldId } from '#lib/ui/fields/types.js';
	import FolderOpenIcon from 'phosphor-svelte/lib/FolderOpenIcon';

	let {
		value = $bindable(''),
		label,
		info,
		// What an empty box comes to, written under it
		unset,
		required = false,
		filter = 'any',
		disabled = false,
		message,
		error,
		icon,
		onchange
	}: { value?: string; label?: string; info?: string; unset?: string; required?: boolean; filter?: BrowseFilter; disabled?: boolean; message?: string; error?: string; icon?: Snippet; onchange?: (v: string) => void } = $props();

	const id = fieldId();
	let open = $state(false);

	function picked(p: string) {
		value = p;
		onchange?.(p);
	}
</script>

{#snippet browse()}
	<NeoButton text {disabled} onclick={() => (open = true)} aria-label="Browse" title="Browse">
		{#snippet icon()}<FolderOpenIcon size={18} />{/snippet}
	</NeoButton>
{/snippet}

<div class="field">
	<FieldLabel {label} {info} forId={id} {required} />
	<NeoInput bind:value {id} {disabled} {required} elevation={-1} placement="inside" floating={false} width="100%" message={error ?? message} {error} autocomplete="off" class="mono-input" before={icon} after={browse} aria-label={label} oninput={() => onchange?.(value)} />
	{#if unset && value === ''}<Resting text={unset} />{/if}
</div>
<DirectoryBrowser bind:open start={value} {filter} onpick={picked} />

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
	:global(.mono-input input) {
		font-family: var(--nebu-font-mono);
		font-size: 13px;
	}
</style>
