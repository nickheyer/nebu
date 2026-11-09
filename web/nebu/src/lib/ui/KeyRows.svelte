<script lang="ts">
	import { capitalize } from '#lib/format.js';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import Tip from '#lib/ui/Tip.svelte';

	export type KeyRow = { label: string; value: string; mono?: boolean; copy?: boolean; tip?: string; href?: string; color?: 'muted' | 'error' | 'warning' | 'success' };

	let { rows, dense = false }: { rows: KeyRow[]; dense?: boolean } = $props();
	const shown = $derived(rows.filter((r) => r.value !== ''));
</script>

<div class="rows" class:dense>
	{#each shown as r (r.label)}
		<div class="kv">
			<span class="k">{capitalize(r.label)}</span>
			<span class="vcell">
				<Tip text={r.tip} tag="span" class="kv-tip">
					{#if r.href}
						<a class="v" class:mono={r.mono} href={r.href}>{r.value}</a>
					{:else}
						<span class="v" class:mono={r.mono} class:error-text={r.color === 'error'} class:warning-text={r.color === 'warning'} class:success-text={r.color === 'success'} class:muted={r.color === 'muted'}>{r.value}</span>
					{/if}
				</Tip>
				{#if r.copy}<CopyButton text={r.value} />{/if}
			</span>
		</div>
	{/each}
</div>

<style>
	/* Labels share one column as wide as the longest so none wraps */
	.rows {
		display: grid;
		grid-template-columns: minmax(12ch, max-content) minmax(0, 1fr);
		column-gap: 12px;
	}
	.kv {
		display: grid;
		grid-template-columns: subgrid;
		grid-column: 1 / -1;
		align-items: center;
		min-height: 32px;
		border-bottom: 1px solid var(--nebu-border);
	}
	.dense .kv {
		min-height: 26px;
		border-bottom: 0;
	}
	.k {
		font-size: 13px;
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
	.vcell {
		display: flex;
		gap: 12px;
		align-items: center;
		min-width: 0;
	}
	:global(.kv-tip) {
		flex: 1 1 auto;
		min-width: 0;
	}
	.v {
		display: inline-block;
		min-width: 0;
		max-width: 100%;
		font-variant-numeric: tabular-nums;
		overflow-wrap: anywhere;
	}
	a.v {
		color: var(--neo-color-primary);
	}
</style>
