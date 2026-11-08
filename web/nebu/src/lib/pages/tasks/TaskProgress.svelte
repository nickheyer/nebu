<script lang="ts">
	import { NeoProgressBar } from '#lib/neo/progress/index.js';
	import { percent } from '#lib/format.js';
	import type { Task } from '#lib/proto/nebu/v1/task_pb.js';
	import Str from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import { active, progressAmount, progressFacts, progressLabel } from '#lib/pages/tasks/tasks.js';

	let { task }: { task: Task } = $props();

	const p = $derived(task.progress);
	const running = $derived(active(task));
	const known = $derived(percent(p?.done, p?.total));
	const amount = $derived(progressAmount(p));
</script>

<div class="progress">
	<div class="head">
		<span class="track"><NeoProgressBar value={known ?? (running ? 0 : 100)} indeterminate={known === undefined && running} elevation={-1} height="8px" width="100%" immediate /></span>
		<span class="pct num">{progressLabel(p)}</span>
	</div>
	{#if p?.message || amount}
		<div class="line">
			<span class="message"><Str value={p?.message ?? ''} kind="message" color="muted" width="100%" /></span>
			{#if amount}<span class="amount num">{amount}</span>{/if}
		</div>
	{/if}
	{#each p?.rows ?? [] as row (row.key)}
		<Tip facts={progressFacts(row)} tag="div" class="taskprogress-row">
			<div class="row">
				<span class="key"><Str value={row.key} kind="text" width="100%" /></span>
				<span class="track"><NeoProgressBar value={percent(row.done, row.total) ?? (running ? 0 : 100)} indeterminate={!row.total && running} elevation={-1} height="4px" width="100%" immediate /></span>
				<span class="pct num">{progressLabel(row)}</span>
			</div>
		</Tip>
	{/each}
</div>

<style>
	.progress {
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 0;
	}
	:global(.taskprogress-row) {
		display: flex;
		width: 100%;
	}
	.head,
	.row {
		display: flex;
		flex: 1 1 auto;
		gap: var(--nebu-space-3);
		align-items: center;
		min-width: 0;
	}
	.track {
		display: flex;
		flex: 1 1 auto;
		min-width: 4rem;
	}
	.key {
		display: flex;
		flex: 0 0 24ch;
		min-width: 0;
		font-size: var(--nebu-text-secondary);
	}
	.pct {
		flex: 0 0 8ch;
		font-size: var(--nebu-text-secondary);
	}
	/* The message leads and the amount reads at the right end under the percent */
	.line {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: baseline;
		min-width: 0;
	}
	.message {
		display: flex;
		flex: 1 1 auto;
		min-width: 0;
	}
	.amount {
		flex: 0 0 auto;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
	}
</style>
