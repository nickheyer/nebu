<script lang="ts">
	import { ConditionStatus, type Condition } from '#lib/proto/nebu/v1/meta_pb.js';
	import { when } from '#lib/format.js';
	import Dot from '#lib/ui/Dot.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import { conditionLamp, reasonWords } from '#lib/ui/status.js';

	let { conditions = [] }: { conditions?: Condition[] } = $props();

	const sorted = $derived(
		[...conditions].sort((a, b) => Number(b.status === ConditionStatus.FALSE) - Number(a.status === ConditionStatus.FALSE))
	);
</script>

{#if sorted.length}
	<div class="conditions">
		{#each sorted as c (c.type)}
			<Tip facts={[{ label: 'message', value: c.message || c.reason }, { label: 'since', value: when(c.lastTransitionTime) }]} tag="div" class="condtip">
				<div class="cond">
					<Dot lamp={conditionLamp(c.status)} />
					<span class="type">{c.type}</span>
					<span class="reason muted">{reasonWords(c.reason)}</span>
				</div>
			</Tip>
		{/each}
	</div>
{:else}
	<span class="secondary">No conditions</span>
{/if}

<style>
	/* Types share one column as wide as the longest, reasons wrap in the rest */
	.conditions {
		display: grid;
		grid-template-columns: max-content max-content minmax(0, 1fr);
		column-gap: 8px;
		min-width: 0;
	}
	:global(.condtip) {
		display: grid;
		grid-template-columns: subgrid;
		grid-column: 1 / -1;
		min-width: 0;
	}
	.cond {
		display: grid;
		grid-template-columns: subgrid;
		grid-column: 1 / -1;
		align-items: baseline;
		min-height: 28px;
		padding: 4px 0;
		font-size: 13px;
	}
	.type {
		font-weight: 500;
	}
	.reason {
		min-width: 0;
		overflow-wrap: anywhere;
	}
</style>
