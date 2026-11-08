<script lang="ts">
	import { bytes } from '#lib/format.js';
	import { shardOrdinal } from '#lib/pages/models/models.js';

	export type Shard = { path: string; index: number; bytes: number | bigint; url?: string };

	// Every shard as one numbered, sized cell, a download where the source allows
	let { shards }: { shards: Shard[] } = $props();
</script>

<div class="tape" role="list">
	{#each shards as s, i (s.path)}
		<svelte:element this={s.url ? 'a' : 'span'} href={s.url} download={s.url ? '' : undefined} class="cell" role="listitem" title={s.path} style:--i={Math.min(i, 24)}>
			<span class="ordinal">{shardOrdinal(s.path, s.index)}</span>
			<span class="size">{bytes(s.bytes)}</span>
		</svelte:element>
	{/each}
</div>

<style>
	.tape {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(6rem, 1fr));
		gap: var(--nebu-space-2);
		padding: var(--nebu-space-2) var(--nebu-row-inset, var(--nebu-space-5)) var(--nebu-space-3);
	}
	.cell {
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding: var(--nebu-space-2) var(--nebu-space-3);
		font-family: var(--nebu-font-mono);
		font-size: var(--nebu-text-secondary);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-screen-text);
		text-decoration: none;
		background: var(--nebu-screen-background);
		border: 1px solid var(--nebu-screen-border);
		border-radius: var(--neo-border-radius);
		box-shadow: var(--nebu-bezel);
		animation: nebu-reveal 320ms var(--nebu-settle) both;
		animation-delay: calc(var(--i) * 14ms);
		transition: border-color var(--nebu-fast) var(--nebu-ease), transform var(--nebu-fast) var(--nebu-settle);
	}
	a.cell:hover {
		border-color: var(--neo-color-primary);
		transform: translateY(-1px);
	}
	.size {
		font-size: var(--nebu-text-caption);
		color: var(--nebu-screen-text-muted);
	}
</style>
