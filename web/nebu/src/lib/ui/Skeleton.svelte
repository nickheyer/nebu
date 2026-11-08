<script lang="ts">
	import Bone from '#lib/ui/Bone.svelte';
	import { toPx } from '#lib/ui/ListRow.svelte';

	// Bones in the layout the loaded data takes
	let {
		rows = 3,
		kind = 'row',
		dot = true,
		leading,
		primary = '24ch',
		secondary = '28ch',
		facts = 1,
		action = true,
		menu = true,
		section = false
	}: {
		rows?: number;
		kind?: 'row' | 'facts' | 'bars' | 'strip' | 'text';
		dot?: boolean;
		leading?: string;
		primary?: string;
		secondary?: string | false;
		facts?: number;
		action?: boolean;
		menu?: boolean;
		section?: boolean;
	} = $props();

	const textWidths = ['90%', '70%', '85%', '60%', '80%', '50%'];
</script>

<div class="skeleton {kind}" class:section aria-busy="true" aria-label="Loading">
	{#each { length: rows } as _, i (i)}
		<div class="line">
			{#if kind === 'row'}
				{#if leading}<Bone width={leading} height="16px" />{/if}
				{#if dot}<Bone width="8px" height="8px" round />{/if}
				<Bone width={toPx(primary)} />
				{#if secondary}<Bone width={toPx(secondary)} />{/if}
				<span class="fill"></span>
				{#each { length: facts } as _, j (j)}<Bone width="7ch" />{/each}
				{#if action}<Bone width="5ch" height="16px" />{/if}
				{#if menu}<Bone width="20px" height="6px" />{/if}
			{:else if kind === 'facts'}
				<span class="k"><Bone width="10ch" /></span>
				<Bone width="20ch" />
			{:else if kind === 'bars'}
				<Bone width="4ch" />
				<Bone width="22ch" />
				<Bone height="10px" grow />
				<Bone width="10ch" />
			{:else if kind === 'strip'}
				{#each { length: 4 } as _, j (j)}
					<span class="cell"><Bone width="7ch" /><Bone width="5ch" height="14px" /></span>
				{/each}
			{:else}
				<Bone width={textWidths[i % textWidths.length] ?? '80%'} />
			{/if}
		</div>
	{/each}
</div>

<style>
	.skeleton {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.line {
		display: flex;
		gap: 12px;
		align-items: center;
		min-width: 0;
	}
	.line > :global(.bone) {
		flex-shrink: 1;
		min-width: 0;
	}
	.fill {
		flex: 1 1 0;
	}
	.row .line {
		height: calc(var(--nebu-row) + 1px);
		padding: var(--nebu-space-1) var(--nebu-space-2);
		border-bottom: 1px solid var(--nebu-border);
	}
	.row .line:last-child {
		border-bottom-color: transparent;
	}
	.facts .line {
		height: 32px;
		border-bottom: 1px solid var(--nebu-border);
	}
	.k {
		display: inline-flex;
		flex: 0 0 14ch;
	}
	.bars .line {
		height: 28px;
		margin-bottom: 8px;
	}
	.bars .line > :global(.bone:nth-child(2)) {
		flex: 0 0 24ch;
		width: 22ch;
	}
	.strip .line {
		gap: 0;
		min-height: calc(var(--nebu-control) + 9px);
		padding: var(--nebu-space-1) 0;
		border-top: 1px solid var(--nebu-border);
		border-bottom: 1px solid var(--nebu-border);
	}
	.strip.section .line {
		min-height: 37px;
		border-top: 0;
	}
	.cell {
		display: flex;
		flex: 1 1 11rem;
		gap: var(--nebu-space-2);
		align-items: center;
		padding: 0 var(--nebu-space-3);
	}
	.cell:first-child {
		padding-left: 0;
	}
	.section .cell {
		flex: 0 0 auto;
	}
	.text .line {
		height: 24px;
	}
</style>
