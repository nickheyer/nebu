<script lang="ts">
  import { pct, bytes } from '$lib/format';
  import type { Task } from '$proto/task_pb';
  import Spinner from './ui/Spinner.svelte';

  // A running task inline: spinner, what it is, and its progress, linking to the task
  let { task, label }: { task: Task; label?: string } = $props();
  const p = $derived(task.progress);
  const known = $derived(!!p?.total);
</script>

<a href="/tasks/{task.id}" class="group inline-flex max-w-full items-center gap-2 text-xs text-accent hover:underline">
  <Spinner size={12} />
  <span class="truncate">{label ?? task.title}</span>
  {#if known}
    <span class="tabular-nums text-fg-muted">{pct(p!.done, p!.total).toFixed(0)}%</span>
    <span class="h-1 w-16 overflow-hidden rounded-full bg-line"><span class="block h-full rounded-full bg-accent" style="width: {pct(p!.done, p!.total)}%"></span></span>
    {#if p!.total >= 100000n}<span class="hidden tabular-nums text-fg-faint group-hover:inline">{bytes(p!.done)}</span>{/if}
  {:else if p?.message}
    <span class="truncate text-fg-faint">{p.message}</span>
  {/if}
</a>
