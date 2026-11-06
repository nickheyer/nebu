<script lang="ts">
	import { NeoProgressBar } from '#lib/neo/progress/index.js';
	import { percent } from '#lib/format.js';
	import type { Task } from '#lib/proto/nebu/v1/task_pb.js';
	import Str from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import { active, progressBytes, progressFacts, progressLabel } from '#lib/pages/tasks/tasks.js';

	let { task }: { task: Task } = $props();

	const p = $derived(task.progress);
	const running = $derived(active(task));
	const known = $derived(percent(p?.done, p?.total));
	const line = $derived([p?.message ?? '', progressBytes(p)].filter(Boolean).join('  '));
</script>

<div class="progress">
	<div class="head">
		<span class="track"><NeoProgressBar value={known ?? (running ? 0 : 100)} indeterminate={known === undefined && running} elevation={-1} height="8px" width="100%" immediate /></span>
		<span class="pct num">{progressLabel(p)}</span>
	</div>
	{#if line}<Str value={line} kind="message" color="muted" width="100%" />{/if}
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
		gap: 12px;
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
		font-size: 13px;
	}
	.pct {
		flex: 0 0 8ch;
		font-size: 13px;
	}
</style>
