<script lang="ts">
	import { ConditionStatus, type Condition } from '#lib/proto/nebu/v1/meta_pb.js';
	import { when } from '#lib/format.js';
	import Dot from '#lib/ui/Dot.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import { passingCondition, reasonWords } from '#lib/ui/status.js';

	// Each check with its reason, filled while it holds, a passing state listed only while it lasts
	let { conditions = [] }: { conditions?: Condition[] } = $props();

	const shown = $derived(conditions.filter((c) => !passingCondition(c.type) || c.status === ConditionStatus.TRUE));
</script>

{#if shown.length}
	<div class="conditions">
		{#each shown as c (c.type)}
			{@const holds = c.status === ConditionStatus.TRUE}
			<Tip facts={[{ label: 'message', value: c.message }, { label: 'since', value: when(c.lastTransitionTime) }].filter((f) => f.value !== '')} placement="top-start" tag="div" class="condtip">
				<div class="cond">
					<Dot lamp={passingCondition(c.type) ? 'warning' : holds ? 'primary' : 'muted'} shape={holds ? 'filled' : 'hollow'} label={holds ? 'holds' : 'does not hold'} />
					<span class="type">{reasonWords(c.type)}</span>
					<span class="reason muted">{reasonWords(c.reason)}</span>
				</div>
			</Tip>
		{/each}
	</div>
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
	.type::first-letter {
		text-transform: uppercase;
	}
	.reason {
		min-width: 0;
		overflow-wrap: anywhere;
	}
</style>
