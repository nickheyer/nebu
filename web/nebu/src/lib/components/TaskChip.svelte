<script lang="ts">
  import { pct, bytes } from '$lib/format';
  import type { Task } from '$proto/task_pb';
  import Spinner from './ui/Spinner.svelte';

  let { task, label }: { task: Task; label?: string } = $props();
  const p = $derived(task.progress);
  const known = $derived(!!p?.total);
</script>

<a href="/tasks?id={task.id}" class="group inline-flex max-w-full items-center gap-2 rounded-md border border-accent/30 bg-accent/10 px-2 py-1 text-xs text-accent hover:bg-accent/15">
  <Spinner size={12} />
  <span class="truncate">{label ?? task.title}</span>
  {#if known}
    <span class="tabular-nums opacity-80">{pct(p!.done, p!.total).toFixed(0)}%</span>
    <span class="h-1 w-16 overflow-hidden rounded-full bg-accent/20"><span class="block h-full bg-accent" style="width: {pct(p!.done, p!.total)}%"></span></span>
    {#if p!.total >= 100000n}<span class="hidden tabular-nums opacity-70 group-hover:inline">{bytes(p!.done)}</span>{/if}
  {:else if p?.message}
    <span class="truncate opacity-80">{p.message}</span>
  {/if}
</a>
