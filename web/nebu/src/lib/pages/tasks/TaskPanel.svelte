<script lang="ts">
	import { reason, rpc } from '#lib/api/transport.js';
	import { duration, enumLabel, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { TaskStateSchema, type Task } from '#lib/proto/nebu/v1/task_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import LogView from '#lib/ui/LogView.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { taskLamp } from '#lib/ui/status.js';
	import TaskProgress from '#lib/pages/tasks/TaskProgress.svelte';
	import { active, madeLink, taskLogs } from '#lib/pages/tasks/tasks.js';

	let { id, onclose }: { id: string; onclose: () => void } = $props();

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
	<PanelHeader title={task.title} subtitle={task.kind} {onclose}>
		<StatePill label={enumLabel(TaskStateSchema, task.state)} lamp={taskLamp(task.state)} />
	</PanelHeader>
	<Stack gap={24}>
		<KeyRows
			rows={[
				{ label: 'created', value: when(task.createdAt) },
				{ label: 'started', value: when(task.startedAt) },
				{ label: 'finished', value: when(task.finishedAt) },
				{ label: 'duration', value: duration(task.startedAt ?? task.createdAt, task.finishedAt, clockTick.now) },
				{ label: 'result', value: link?.label ?? '', href: link?.href }
			]}
		/>
		{#if task.error}<p class="error-text failure">{task.error}</p>{/if}
		{#if task.progress && running}
			<Section title="Progress" level={3}>
				<TaskProgress {task} />
			</Section>
		{/if}
		<Section title="Log" level={3}>
			{#key `${id}:${running}`}
				<LogView source={taskLogs(id, running)} name={task.title} height="40vh" />
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
	.failure {
		overflow-wrap: anywhere;
		white-space: pre-wrap;
	}
</style>
