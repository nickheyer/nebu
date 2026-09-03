<script lang="ts">
  import { api, message } from '$lib/api';
  import { enumName, human } from '$lib/format';
  import { TaskState, type Task } from '$proto/task_pb';
  import Badge from './Badge.svelte';

  let { id, onDone }: { id: string; onDone?: (t: Task) => void } = $props();
  let task = $state<Task | null>(null);
  let lines = $state<string[]>([]);
  let error = $state('');
  let box: HTMLDivElement | undefined = $state();

  $effect(() => {
    const controller = new AbortController();
    lines = [];
    task = null;
    error = '';
    (async () => {
      try {
        for await (const msg of api.tasks.watchTask({ id }, { signal: controller.signal })) {
          if (msg.task) task = msg.task;
          if (msg.logs.length) {
            lines = [...lines, ...msg.logs];
            queueMicrotask(() => box?.scrollTo({ top: box.scrollHeight }));
          }
        }
        if (task && onDone) onDone(task);
      } catch (err) {
        if (!controller.signal.aborted) error = message(err);
      }
    })();
    return () => controller.abort();
  });

  function progress(t: Task): string {
    const p = t.progress;
    if (!p || !p.total) return p?.message ?? '';
    const small = p.total < 1000n;
    const pct = Number((p.done * 100n) / p.total);
    return (small ? `${p.done}/${p.total}` : `${human(p.done)} / ${human(p.total)} ${pct}%`) + (p.message ? '  ' + p.message : '');
  }

  async function cancel() {
    try {
      await api.tasks.cancelTask({ id });
    } catch (err) {
      error = message(err);
    }
  }
</script>

<div class="space-y-2">
  {#if task}
    <div class="flex flex-wrap items-center gap-2 text-sm">
      <Badge state={enumName(TaskState, task.state)} />
      <span class="font-medium">{task.title}</span>
      <span class="muted">{progress(task)}</span>
      {#if task.state === TaskState.RUNNING || task.state === TaskState.PENDING}
        <button class="btn ml-auto" onclick={cancel}>cancel</button>
      {/if}
    </div>
    {#if task.progress?.total}
      <div class="h-1.5 w-full overflow-hidden rounded bg-zinc-800">
        <div class="h-full bg-emerald-600" style="width: {Number((task.progress.done * 100n) / task.progress.total)}%"></div>
      </div>
    {/if}
    {#if task.error}<div class="text-sm text-red-300">{task.error}</div>{/if}
  {/if}
  {#if error}<div class="text-sm text-red-300">{error}</div>{/if}
  <div class="log" bind:this={box}>{lines.join('\n')}</div>
</div>
