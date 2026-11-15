<script lang="ts">
	import { ConditionStatus, type Condition } from '#lib/proto/nebu/v1/meta_pb.js';
	import { when } from '#lib/format.js';
	import Dot from '#lib/ui/Dot.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import { failingReason, passingCondition, reasonWords, type DotShape, type Lamp } from '#lib/ui/status.js';

	// Each check with its reason, green while it holds, red with its message when it fails, grey when it does not apply, a passing state listed only while it lasts
	let { conditions = [] }: { conditions?: Condition[] } = $props();

	type Row = { type: string; words: string; lamp: Lamp; shape: DotShape; state: string; message: string; since: string };

	const rows = $derived(
		conditions
			.filter((c) => !passingCondition(c.type) || c.status === ConditionStatus.TRUE)
			.map((c): Row => {
				const holds = c.status === ConditionStatus.TRUE;
				const failed = !holds && !passingCondition(c.type) && failingReason(c.reason);
				const type = reasonWords(c.type);
				const reason = reasonWords(c.reason);
				return {
					type,
					// A reason that only repeats the check's name says nothing more
					words: reason === type ? '' : reason,
					lamp: passingCondition(c.type) ? 'warning' : holds ? 'success' : failed ? 'error' : 'muted',
					shape: holds || failed ? 'filled' : 'hollow',
					state: passingCondition(c.type) ? 'under way' : holds ? 'holds' : failed ? 'failing' : 'does not apply',
					message: failed || passingCondition(c.type) ? c.message : '',
					since: when(c.lastTransitionTime)
				};
			})
	);
</script>

{#if rows.length}
	<div class="conditions">
		{#each rows as r (r.type)}
			<Tip facts={[{ label: 'state', value: r.state }, { label: 'since', value: r.since }].filter((f) => f.value !== '')} placement="top-start" tag="div" class="condtip">
				<div class="cond">
					<Dot lamp={r.lamp} shape={r.shape} label={r.state} />
					<span class="type">{r.type}</span>
					<span class="reason" class:muted={r.lamp !== 'error'} class:error-text={r.lamp === 'error'}>{[r.words, r.message].filter(Boolean).join(', ') || r.state}</span>
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
