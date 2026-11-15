<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { capitalize, date, duration, enumLabel, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { TaskStateSchema, type Task } from '#lib/proto/nebu/v1/task_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows, { type KeyRow } from '#lib/ui/KeyRows.svelte';
	import LogView from '#lib/ui/LogView.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { taskLamp } from '#lib/ui/status.js';
	import TaskProgress from '#lib/pages/tasks/TaskProgress.svelte';
	import { active, madeLink, taskLogs, type RunPlace } from '#lib/pages/tasks/tasks.js';
	import CaretLeftIcon from 'phosphor-svelte/lib/CaretLeftIcon';
	import CaretRightIcon from 'phosphor-svelte/lib/CaretRightIcon';

	// One task with its times, its log and the way to what it made, and where it stands when it is one of several runs in a row
	let { id, place, onclose }: { id: string; place?: RunPlace; onclose: () => void } = $props();

	let fetched = $state<Task | undefined>(undefined);
	let error = $state('');
	const task = $derived(live.tasks.get(id) ?? fetched);
	const link = $derived(task ? madeLink(task) : undefined);

	// Fetches a task the live mirror does not hold
	$effect(() => {
		const want = id;
		fetched = undefined;
		error = '';
		if (live.tasks.has(want)) return;
		void rpc.tasks
			.getTask({ id: want })
			.then((r) => (fetched = r.task))
			.catch((err) => (error = reason(err)));
	});

	// When the task was made, began and ended, the date said once when all three share it, and where it wrote when it wrote to a directory
	function times(t: Task): KeyRow[] {
		const day = (ts: Task['createdAt']) => date(ts)?.toDateString();
		const same = [t.startedAt, t.finishedAt].every((ts) => !ts || day(ts) === day(t.createdAt));
		const at = (ts: Task['createdAt']) => (!ts ? '' : same ? (date(ts)?.toLocaleTimeString() ?? '') : when(ts));
		return [
			{ label: 'date', value: same ? (date(t.createdAt)?.toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' }) ?? '') : '' },
			{ label: 'queued', value: at(t.createdAt), tip: when(t.createdAt) },
			{ label: 'started', value: at(t.startedAt), tip: when(t.startedAt) },
			{ label: 'finished', value: at(t.finishedAt), tip: when(t.finishedAt) },
			{ label: 'took', value: duration(t.startedAt ?? t.createdAt, t.finishedAt, clockTick.now) },
			{ label: 'wrote to', value: t.labels['dir'] ?? '', mono: true, copy: true }
		];
	}

	async function cancel() {
		try {
			await rpc.tasks.cancelTask({ id });
		} catch (err) {
			toast.error('Stop failed', reason(err));
		}
	}
</script>

{#if task}
	{@const running = active(task)}
	<PanelHeader title={capitalize(task.title)} lamp={taskLamp(task.state)} state={enumLabel(TaskStateSchema, task.state)} subtitle={place ? (place.at === 1 ? `the latest of ${place.of} runs in a row` : `run ${place.of - place.at + 1} of ${place.of} in a row`) : undefined} {onclose}>
		{#snippet tools()}
			{#if link}<NeoButton elevation={2} href={link.href}>{link.label}</NeoButton>{/if}
		{/snippet}
	</PanelHeader>
	<Stack gap={24}>
		{#if place}
			<div class="runs">
				<NeoButton text disabled={!place.older} href={place.older ? `/tasks/${place.older}` : undefined}>{#snippet icon()}<CaretLeftIcon size={16} />{/snippet}Earlier run</NeoButton>
				<NeoButton text disabled={!place.newer} href={place.newer ? `/tasks/${place.newer}` : undefined}>Later run<CaretRightIcon size={16} /></NeoButton>
			</div>
		{/if}
		<KeyRows rows={times(task)} />
		{#if task.error}<p class="error-text failure">{task.error}</p>{/if}
		{#if task.progress && running}
			<Section title="Progress" level={3}>
				<TaskProgress {task} />
			</Section>
		{/if}
		<Section title="Log" level={3}>
			{#key `${id}:${running}`}
				<LogView source={taskLogs(id, running)} name={task.title} levels growing={running} height="40vh" />
			{/key}
		</Section>
		{#if running}
			<div class="actions"><Confirm label="Stop" question="Stop {task.title}?" danger placement="top-start" onconfirm={cancel} /></div>
		{/if}
	</Stack>
{:else if error}
	<PanelHeader title="Task" {onclose} />
	<Empty text={error} action="Back" onclick={onclose} />
{:else}
	<PanelHeader title="Task" {onclose} />
	<Skeleton rows={5} kind="facts" />
{/if}

<style>
	.actions {
		display: flex;
		justify-content: flex-start;
	}
	.runs {
		display: flex;
		gap: var(--nebu-space-2);
		justify-content: space-between;
		margin: calc(-1 * var(--nebu-space-3)) 0;
	}
	.failure {
		overflow-wrap: anywhere;
		white-space: pre-wrap;
	}
</style>
