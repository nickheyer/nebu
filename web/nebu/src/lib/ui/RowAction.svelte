<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import PlayIcon from 'phosphor-svelte/lib/PlayIcon';

	// The one quiet action of a row, a bordered chip, red when it removes, or the accent play glyph
	let {
		label,
		href,
		onclick,
		disabled = false,
		loading = false,
		tone = 'default',
		play = false
	}: { label: string; href?: string; onclick?: () => unknown; disabled?: boolean; loading?: boolean; tone?: 'default' | 'danger'; play?: boolean } = $props();
</script>

{#if play}
	<NeoButton text class="rowplay" aria-label={label} {href} {onclick} {disabled} {loading}>{#snippet icon()}<PlayIcon size={20} weight="fill" />{/snippet}</NeoButton>
{:else}
	<NeoButton elevation={0} class="rowaction {tone === 'danger' ? 'danger' : ''}" {href} {onclick} {disabled} {loading}>{label}</NeoButton>
{/if}

<style>
	:global(.neo-button.rowaction) {
		--neo-btn-text-color: var(--nebu-text-muted);
		--neo-btn-text-color-hover: var(--neo-text-color);
		--neo-btn-text-color-active: var(--neo-text-color);
		--neo-btn-border-color: var(--nebu-border-strong);
		--neo-btn-border-color-hover: var(--neo-color-primary);
		--neo-btn-padding: 0.25rem 0.75rem;

		min-height: var(--nebu-row-dense);
		font-size: var(--nebu-text-secondary);
		white-space: nowrap;
	}
	:global(.neo-button.rowplay) {
		--neo-btn-text-color: var(--neo-color-primary);
		--neo-btn-text-color-hover: var(--neo-color-primary);
		--neo-btn-text-color-active: var(--neo-color-primary);
	}
</style>
