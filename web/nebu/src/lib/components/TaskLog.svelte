<script lang="ts">
  import { api, message } from '$lib/api';
  import { live, clock, taskActive } from '$lib/state.svelte';
  import { bytes, count, duration, pct } from '$lib/format';
  import { TaskState, type Task } from '$proto/task_pb';
  import { Ban } from '@lucide/svelte';
  import StateBadge from './ui/StateBadge.svelte';
  import Button from './ui/Button.svelte';
  import Progress from './ui/Progress.svelte';
  import LogView from './ui/LogView.svelte';
  import { fail } from '$lib/toast.svelte';

  let { id, height = 'h-80' }: { id: string; height?: string } = $props();

  let streamed = $state<Task | null>(null);
  let lines = $state<string[]>([]);
  let error = $state('');
  let cancelling = $state(false);

  const task = $derived(live.tasks.get(id) ?? streamed);
  const active = $derived(!!task && taskActive(task));
  const progress = $derived.by(() => {
    const p = task?.progress;
    if (!p) return { known: false, text: '' };
    if (!p.total) return { known: false, text: p.message };
    const asBytes = p.total >= 100000n;
    const text = asBytes ? `${bytes(p.done)} of ${bytes(p.total)}` : `${count(p.done)} of ${count(p.total)}`;
    return { known: true, text: `${text} · ${pct(p.done, p.total).toFixed(0)}%${p.message ? ' · ' + p.message : ''}` };
  });

  $effect(() => {
    const controller = new AbortController();
    const current = id;
    lines = [];
    streamed = null;
    error = '';
    (async () => {
      try {
        for await (const msg of api.tasks.watchTask({ id: current }, { signal: controller.signal })) {
          if (msg.task) streamed = msg.task;
          if (msg.logs.length) {
            lines.push(...msg.logs);
            if (lines.length > 5000) lines.splice(0, lines.length - 5000);
          }
        }
        // A finished task answers from history, so read the stored log if the stream carried none
        if (lines.length === 0 && !controller.signal.aborted) {
          const stored = await api.tasks.getTask({ id: current }, { signal: controller.signal });
          if (stored.task && !streamed) streamed = stored.task;
          lines.push(...stored.logs);
        }
      } catch (err) {
        if (!controller.signal.aborted) error = message(err);
      }
    })();
    return () => controller.abort();
  });

  async function cancel() {
    cancelling = true;
    try {
      await api.tasks.cancelTask({ id });
    } catch (err) {
      fail(err, 'Cancel failed');
    } finally {
      cancelling = false;
    }
  }
</script>

<div class="flex flex-col gap-3">
  {#if task}
    <div class="flex flex-wrap items-center gap-2">
      <StateBadge values={TaskState} value={task.state} />
      <span class="font-medium text-fg">{task.title}</span>
      <span class="rounded bg-raised px-1.5 py-0.5 font-mono text-[11px] text-fg-muted">{task.kind}</span>
      <span class="text-xs text-fg-faint tabular-nums">{duration(task.startedAt ?? task.createdAt, task.finishedAt, clock.now)}</span>
      {#if active}
        <Button size="xs" variant="ghost" icon={Ban} class="ml-auto" loading={cancelling} onclick={cancel}>Cancel</Button>
      {/if}
    </div>
    {#if active || progress.known}
      <div class="flex flex-col gap-1.5">
        <Progress done={task.progress?.done} total={task.progress?.total} {active} tone={task.state === TaskState.FAILED ? 'bad' : task.state === TaskState.SUCCEEDED ? 'ok' : 'accent'} />
        {#if progress.text}<div class="text-xs text-fg-muted tabular-nums">{progress.text}</div>{/if}
      </div>
    {/if}
    {#if task.error}
      <div class="rounded-md border border-bad/30 bg-bad/10 px-3 py-2 text-sm leading-6 text-bad">{task.error}</div>
    {/if}
  {/if}
  {#if error}<div class="text-sm text-bad">{error}</div>{/if}
  <LogView {lines} {height} live={active} empty={active ? 'Waiting for output' : 'No output was recorded'} />
</div>
