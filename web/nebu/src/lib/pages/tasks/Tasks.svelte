<script lang="ts">
	import { goto } from '$app/navigation';
	import { NeoProgressBar } from '#lib/neo/progress/index.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ago, duration, enumLabel, enumOptions, num, percent, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { TaskState, TaskStateSchema, type Task } from '#lib/proto/nebu/v1/task_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import Mark, { type MarkState } from '#lib/ui/Mark.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { hostTabs } from '#lib/pages/host/tabs.js';
	import TaskPanel from '#lib/pages/tasks/TaskPanel.svelte';
	import { active, progressFacts, progressLabel, taskKinds } from '#lib/pages/tasks/tasks.js';
	import ListChecksIcon from 'phosphor-svelte/lib/ListChecksIcon';

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

	// Runs of the same task back to back fold into one row
	const groups = $derived.by(() => {
		const out: { head: Task; rest: Task[] }[] = [];
		for (const t of history.items) {
			const last = out.at(-1);
			if (last && last.head.kind === t.kind && last.head.title === t.title && last.head.state === t.state) last.rest.push(t);
			else out.push({ head: t, rest: [] });
		}
		return out;
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

	function markOf(t: Task): MarkState {
		if (t.state === TaskState.SUCCEEDED) return 'ok';
		if (t.state === TaskState.FAILED) return 'failed';
		if (active(t)) return 'active';
		return 'skipped';
	}

	function historyFacts(t: Task) {
		return [
			{ value: duration(t.startedAt ?? t.createdAt, t.finishedAt, clockTick.now), tip: 'duration' },
			{ value: t.finishedAt ? ago(t.finishedAt, clockTick.now) : '', tip: `finished ${when(t.finishedAt)}` }
		];
	}
</script>

{#snippet bar(t: Task)}
	{@const known = percent(t.progress?.done, t.progress?.total)}
	<Tip facts={progressFacts(t.progress)} tag="span" class="taskbar">
		<span class="track"><NeoProgressBar value={known ?? 0} indeterminate={known === undefined} elevation={-1} height="8px" width="100%" immediate /></span>
		<span class="pct num">{progressLabel(t.progress)}</span>
	</Tip>
{/snippet}

{#snippet kindPill(t: Task)}
	<span class="kindcol"><StatePill label={t.kind} /></span>
{/snippet}

{#snippet historyLeading(t: Task)}
	<Mark state={markOf(t)} label={enumLabel(TaskStateSchema, t.state)} tip={t.state === TaskState.FAILED ? t.error || undefined : enumLabel(TaskStateSchema, t.state)} />
	{@render kindPill(t)}
{/snippet}

{#snippet activeCard()}
	<Card title="Running" flush>
		<List head={{ primary: 'Task', instrument: 'Progress', action: 'Elapsed' }}>
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
				<ListRow primary={t.title} primaryKind="text" primaryWidth="32ch" selected={t.id === id} onclick={() => open(t)} {instrument} expand={rows.length ? progressRows : undefined}>
					{#snippet leading()}{@render kindPill(t)}{/snippet}
					{#snippet action()}
						<span class="elapsed num secondary"><Tip text="started {when(t.startedAt ?? t.createdAt)}" tag="span">{t.state === TaskState.PENDING ? 'queued' : duration(t.startedAt ?? t.createdAt, undefined, clockTick.now)}</Tip></span>
						<Confirm label="Cancel" question="Cancel {t.title}?" danger onconfirm={() => cancel(t)} />
					{/snippet}
				</ListRow>
			{/each}
		</List>
	</Card>
{/snippet}

{#snippet historyCard()}
	<Card title="History" flush>
		{#snippet tools()}
			<span class="filter"><Select bind:value={kind} options={kindOptions} label="Kind" compact clearable /></span>
			<span class="filter"><Select bind:value={taskState} options={stateOptions} label="State" compact clearable /></span>
		{/snippet}
		{#if !history.settled}
			<Skeleton rows={6} dot={false} leading="12ch" primary="32ch" secondary={false} facts={2} action={false} menu={false} />
		{:else if history.error}
			<Empty text={history.error} action="Retry" onclick={() => history.refresh()} />
		{:else if history.items.length === 0}
			<Empty icon={ListChecksIcon} text={kind || taskState !== undefined ? 'No tasks match' : 'No tasks'} />
		{:else}
			<List head={{ primary: 'Task', facts: ['Duration', 'Finished'] }}>
				{#each groups as g (g.head.id)}
					{#snippet runs()}
						{#each g.rest as t (t.id)}
							<ListRow primary={when(t.finishedAt ?? t.createdAt)} primaryKind="text" facts={historyFacts(t)} selected={t.id === id} onclick={() => open(t)}>
								{#snippet leading()}<Mark state={markOf(t)} label={enumLabel(TaskStateSchema, t.state)} />{/snippet}
							</ListRow>
						{/each}
					{/snippet}
					<ListRow primary={g.head.title} primaryKind="text" primaryWidth="32ch" facts={historyFacts(g.head)} selected={g.head.id === id} onclick={() => open(g.head)} expand={g.rest.length ? runs : undefined}>
						{#snippet leading()}{@render historyLeading(g.head)}{/snippet}
						{#snippet badges()}
							{#if g.rest.length}<span class="repeat" title="{g.rest.length + 1} runs in a row">×{num(g.rest.length + 1)}</span>{/if}
						{/snippet}
					</ListRow>
				{/each}
			</List>
			<LoadMore next={history.next} loading={history.loading} onmore={() => history.more()} />
		{/if}
	</Card>
{/snippet}

{#snippet list()}
	{#if running.length}{@render activeCard()}{/if}
	{@render historyCard()}
{/snippet}

<PageHeader tabs={hostTabs} active="tasks" info="Background work the daemon runs, pulls, builds, verifies and probes, live and past" />

<Sheet open={id !== ''} onclose={() => goto('/tasks')} {list}>
	{#if id}
		<TaskPanel {id} onclose={() => goto('/tasks')} />
	{/if}
</Sheet>

<style>
	:global(.taskbar) {
		display: inline-flex;
		flex: 1 1 auto;
		gap: var(--nebu-space-2);
		align-items: center;
		width: 100%;
	}
	:global(.taskrow-tip) {
		display: flex;
		width: 100%;
	}
	.kindcol {
		display: inline-flex;
		flex: 0 0 auto;
		min-width: 9ch;
	}
	.filter {
		display: inline-flex;
		width: 10rem;
	}
	.repeat {
		font-size: var(--nebu-text-caption);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-text-muted);
	}
	.elapsed {
		min-width: 6ch;
	}
	.track {
		display: flex;
		flex: 1 1 auto;
		min-width: 4rem;
	}
	.pct {
		flex: 0 0 6ch;
		min-width: 6ch;
		font-size: var(--nebu-text-secondary);
	}
	.prow {
		display: flex;
		flex: 1 1 auto;
		gap: var(--nebu-space-3);
		align-items: center;
		min-width: 0;
		min-height: var(--nebu-space-5);
	}
	.key {
		display: flex;
		flex: 0 0 24ch;
		min-width: 0;
		font-size: var(--nebu-text-secondary);
	}
</style>
