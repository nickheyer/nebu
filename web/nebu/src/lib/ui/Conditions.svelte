<script lang="ts">
	import { ConditionStatus, type Condition } from '#lib/proto/nebu/v1/meta_pb.js';
	import { when } from '#lib/format.js';
	import Dot from '#lib/ui/Dot.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import { conditionLamp } from '#lib/ui/status.js';

	let { conditions = [] }: { conditions?: Condition[] } = $props();

	const sorted = $derived(
		[...conditions].sort((a, b) => Number(b.status === ConditionStatus.FALSE) - Number(a.status === ConditionStatus.FALSE))
	);
</script>

{#if sorted.length}
	<div class="conditions">
		{#each sorted as c (c.type)}
			<Tip facts={[{ label: 'message', value: c.message || c.reason }, { label: 'since', value: when(c.lastTransitionTime) }]} tag="div">
				<div class="cond">
					<Dot lamp={conditionLamp(c.status)} />
					<span class="type">{c.type}</span>
					<span class="reason muted ellipsis">{c.reason}</span>
				</div>
			</Tip>
		{/each}
	</div>
{:else}
	<span class="secondary">No conditions</span>
{/if}

<style>
	.conditions {
		display: flex;
		flex-direction: column;
	}
	.cond {
		display: flex;
		gap: 8px;
		align-items: center;
		min-height: 28px;
		font-size: 13px;
	}
	.type {
		flex: 0 0 16ch;
		font-weight: 500;
	}
	.reason {
		min-width: 0;
	}
</style>
