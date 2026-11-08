<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';

	// The one quiet action of a row, red for stop or remove
	let {
		label,
		href,
		onclick,
		disabled = false,
		loading = false,
		tone = 'default'
	}: { label: string; href?: string; onclick?: () => unknown; disabled?: boolean; loading?: boolean; tone?: 'default' | 'danger' } = $props();
</script>

<NeoButton text class="rowaction {tone === 'danger' ? 'danger' : ''}" {href} {onclick} {disabled} {loading}>{label}</NeoButton>

<style>
	:global(.neo-button.rowaction) {
		--neo-btn-text-color: var(--nebu-text-muted);
		--neo-btn-text-color-hover: var(--neo-text-color);
		--neo-btn-text-color-active: var(--neo-text-color);
		min-height: var(--nebu-row-dense);
		font-size: var(--nebu-text-secondary);
		white-space: nowrap;
	}
</style>
