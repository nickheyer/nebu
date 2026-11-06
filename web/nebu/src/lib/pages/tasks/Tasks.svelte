<script lang="ts">
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoProgressBar } from '#lib/neo/progress/index.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ago, duration, enumLabel, enumOptions, percent, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { TaskState, TaskStateSchema, type Task } from '#lib/proto/nebu/v1/task_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { taskLamp } from '#lib/ui/status.js';
	import TaskPanel from '#lib/pages/tasks/TaskPanel.svelte';
	import { active, progressFacts, progressLabel, taskKinds } from '#lib/pages/tasks/tasks.js';
	import CheckIcon from 'phosphor-svelte/lib/CheckIcon';
	import HourglassIcon from 'phosphor-svelte/lib/HourglassIcon';
	import ProhibitIcon from 'phosphor-svelte/lib/ProhibitIcon';
	import XIcon from 'phosphor-svelte/lib/XIcon';

	let { id = '' }: { id?: string } = $props();

	let kind = $state<string | undefined>(undefined);
	let taskState = $state<TaskState | undefined>(undefined);

	const running = $derived([...live.tasks.values()].filter(active).sort((a, b) => Number(b.createdAt?.seconds ?? 0n) - Number(a.createdAt?.seconds ?? 0n)));

	const history = new ListQuery<Task, { kind?: string; state?: TaskState }>(
		async (p, cursor, limit, signal) => {
			const res = await rpc.tasks.listTasks({ kind: p.kind ?? '', state: p.state ?? TaskState.UNSPECIFIED, cursor, limit }, { signal });
			return { items: res.tasks, next: res.nextCursor };
		},
		[EventKind.TASK]
	);

	$effect(() => {
		void history.load({ kind, state: taskState });
	});

	async function cancel(t: Task) {
		try {
			await rpc.tasks.cancelTask({ id: t.id });
		} catch (err) {
			toast.error('Cancel failed', reason(err));
		}
	}

	function open(t: Task) {
		void goto(`/tasks/${t.id}`);
	}

	const kindOptions = taskKinds.map((k) => ({ value: k, label: k }));
	const stateOptions = enumOptions(TaskStateSchema).map((o) => ({ value: o.value as TaskState, label: o.label }));

	function markLabel(t: Task): string {
		return enumLabel(TaskStateSchema, t.state);
	}
</script>

{#snippet bar(t: Task)}
	{@const known = percent(t.progress?.done, t.progress?.total)}
	<Tip facts={progressFacts(t.progress)} tag="span" class="taskbar">
		<span class="track"><NeoProgressBar value={known ?? 0} indeterminate={known === undefined} elevation={-1} height="8px" width="100%" immediate /></span>
		<span class="pct num">{progressLabel(t.progress)}</span>
	</Tip>
{/snippet}

{#snippet activeSection()}
	<Section title="Active" count={running.length}>
		{#if !live.ready}
			<Skeleton rows={2} />
		{:else if running.length === 0}
			<Empty text="Nothing running" />
		{:else}
			{#each running as t (t.id)}
				{@const rows = t.progress?.rows ?? []}
				{#snippet instrument()}{@render bar(t)}{/snippet}
				{#snippet progressRows()}
					{#each rows as row (row.key)}
						<Tip facts={progressFacts(row)} tag="div" class="taskrow-tip">
							<div class="prow">
								<span class="key"><Str value={row.key} kind="text" width="100%" /></span>
								<span class="track"><NeoProgressBar value={percent(row.done, row.total) ?? 0} indeterminate={!row.total} elevation={-1} height="4px" width="100%" immediate /></span>
								<span class="pct num">{progressLabel(row)}</span>
							</div>
						</Tip>
					{/each}
				{/snippet}
				<ListRow
					primary={t.title}
					primaryKind="text"
					primaryWidth="40ch"
					facts={[{ value: duration(t.startedAt ?? t.createdAt, undefined, clockTick.now), tip: `started ${when(t.startedAt ?? t.createdAt)}` }]}
					selected={t.id === id}
					onclick={() => open(t)}
					{instrument}
					expand={rows.length ? progressRows : undefined}
				>
					{#snippet leading()}<StatePill label={t.kind} />{/snippet}
					{#snippet action()}<Confirm label="Cancel" question="Cancel {t.title}?" danger onconfirm={() => cancel(t)} />{/snippet}
				</ListRow>
			{/each}
		{/if}
	</Section>
{/snippet}

{#snippet historySection()}
	<Section title="History">
		{#snippet tools()}
			<Row gap={12} justify="flex-end" wrap={false}>
				<Cell min="8rem" max="11rem"><Select bind:value={kind} options={kindOptions} placeholder="Kind" clearable /></Cell>
				<Cell min="8rem" max="11rem"><Select bind:value={taskState} options={stateOptions} placeholder="State" clearable /></Cell>
			</Row>
		{/snippet}
		{#if !history.settled}
			<Skeleton rows={4} />
		{:else if history.error}
			<Empty text={history.error} action="Retry" onclick={() => history.refresh()} />
		{:else if history.items.length === 0}
			<Empty text="No tasks" />
		{:else}
			{#each history.items as t (t.id)}
				{@const failed = t.state === TaskState.FAILED}
				<ListRow
					primary={t.title}
					primaryKind="text"
					primaryWidth="40ch"
					facts={[
						{ value: duration(t.startedAt ?? t.createdAt, t.finishedAt, clockTick.now) },
						{ value: t.finishedAt ? ago(t.finishedAt, clockTick.now) : '', tip: `finished ${when(t.finishedAt)}` }
					]}
					selected={t.id === id}
					onclick={() => open(t)}
				>
					{#snippet leading()}
						<Tip text={failed ? t.error : undefined} tag="span">
							<span class="mark {taskLamp(t.state)}" role="img" aria-label={markLabel(t)}>
								{#if t.state === TaskState.SUCCEEDED}<CheckIcon size={20} />{:else if failed}<XIcon size={20} />{:else if active(t)}<HourglassIcon size={20} />{:else}<ProhibitIcon size={20} />{/if}
							</span>
						</Tip>
						<StatePill label={t.kind} />
					{/snippet}
					{#snippet action()}<NeoButton text href="/tasks/{t.id}">Open</NeoButton>{/snippet}
				</ListRow>
			{/each}
			<LoadMore next={history.next} loading={history.loading} onmore={() => history.more()} />
		{/if}
	</Section>
{/snippet}

{#snippet list()}
	<Stack gap={24}>
		{@render activeSection()}
		{@render historySection()}
	</Stack>
{/snippet}

<Sheet open={id !== ''} onclose={() => goto('/tasks')} {list} empty="Select a task">
	{#if id}
		<TaskPanel {id} onclose={() => goto('/tasks')} />
	{/if}
</Sheet>

<style>
	:global(.taskbar) {
		display: inline-flex;
		flex: 1 1 auto;
		gap: 8px;
		align-items: center;
		width: 100%;
	}
	:global(.taskrow-tip) {
		display: flex;
		width: 100%;
	}
	.track {
		display: flex;
		flex: 1 1 auto;
		min-width: 4rem;
	}
	.pct {
		flex: 0 0 6ch;
		min-width: 6ch;
		font-size: 13px;
	}
	.prow {
		display: flex;
		flex: 1 1 auto;
		gap: 12px;
		align-items: center;
		min-width: 0;
		min-height: 24px;
	}
	.key {
		display: flex;
		flex: 0 0 24ch;
		min-width: 0;
		font-size: 13px;
	}
	.mark {
		display: inline-flex;
	}
	.mark.success {
		color: var(--neo-color-success);
	}
	.mark.error {
		color: var(--neo-color-error);
	}
	.mark.warning {
		color: var(--neo-color-warning);
	}
	.mark.muted {
		color: var(--nebu-text-muted);
	}
</style>
