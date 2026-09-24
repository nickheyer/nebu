<script lang="ts">
  import { api } from '$lib/api';
  import { activeTasks, clock } from '$lib/state.svelte';
  import { pct, bytes, duration } from '$lib/format';
  import { fail } from '$lib/toast.svelte';
  import type { Task } from '$proto/task_pb';
  import { Ban } from '@lucide/svelte';
  import Card from './ui/Card.svelte';
  import Progress from './ui/Progress.svelte';
  import Spinner from './ui/Spinner.svelte';
  import IconButton from './ui/IconButton.svelte';

  const tasks = $derived(activeTasks());

  async function cancel(t: Task) {
    try {
      await api.tasks.cancelTask({ id: t.id });
    } catch (err) {
      fail(err, 'Cancel failed');
    }
  }
</script>

{#if tasks.length}
  <Card title="Activity" meta={`${tasks.length} running`} padded={false}>
    <ul class="divide-y divide-line/70 px-4">
      {#each tasks as t (t.id)}
        <li class="flex items-center gap-4 py-2.5">
          <Spinner size={14} class="shrink-0 text-accent" />
          <a href="/tasks/{t.id}" class="min-w-0 flex-1">
            <div class="flex items-baseline gap-3">
              <span class="truncate text-sm text-fg hover:text-accent">{t.title}</span>
              <span class="ml-auto shrink-0 text-xs tabular-nums text-fg-faint">{duration(t.startedAt ?? t.createdAt, undefined, clock.now)}</span>
            </div>
            <Progress done={t.progress?.done} total={t.progress?.total} active class="mt-1.5" />
            <div class="mt-1 truncate text-xs tabular-nums text-fg-muted">
              {#if t.progress?.total}
                {pct(t.progress.done, t.progress.total).toFixed(0)}%{#if t.progress.total >= 100000n}<span>{' · '}{bytes(t.progress.done)} of {bytes(t.progress.total)}</span>{/if}
              {/if}
              {#if t.progress?.message}<span>{t.progress?.total ? ' · ' : ''}{t.progress.message}</span>{/if}
            </div>
          </a>
          <IconButton size="xs" icon={Ban} label="Cancel" onclick={() => cancel(t)} />
        </li>
      {/each}
    </ul>
  </Card>
{/if}
