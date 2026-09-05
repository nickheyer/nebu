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
  import Button from './ui/Button.svelte';

  // What the daemon is busy with right now, each row leading to its log
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
  <Card title="In progress" description="{tasks.length} running" href="/tasks" flush>
    <ul class="divide-y divide-line/60">
      {#each tasks as t (t.id)}
        <li class="flex items-center gap-4 px-5 py-3">
          <Spinner size={16} class="shrink-0 text-accent" />
          <a href="/tasks?id={t.id}" class="min-w-0 flex-1">
            <div class="flex items-baseline gap-3">
              <span class="truncate text-sm font-medium text-fg hover:text-accent">{t.title}</span>
              <span class="ml-auto shrink-0 text-xs tabular-nums text-fg-faint">{duration(t.startedAt ?? t.createdAt, undefined, clock.now)}</span>
            </div>
            <Progress done={t.progress?.done} total={t.progress?.total} active class="mt-2" />
            <div class="mt-1 truncate text-xs tabular-nums text-fg-muted">
              {#if t.progress?.total}
                {pct(t.progress.done, t.progress.total).toFixed(0)}%{#if t.progress.total >= 100000n}<span>{' · '}{bytes(t.progress.done)} of {bytes(t.progress.total)}</span>{/if}
              {/if}
              {#if t.progress?.message}<span>{t.progress?.total ? ' · ' : ''}{t.progress.message}</span>{/if}
            </div>
          </a>
          <Button size="sm" variant="ghost" icon={Ban} aria-label="Cancel {t.title}" onclick={() => cancel(t)} />
        </li>
      {/each}
    </ul>
  </Card>
{/if}
