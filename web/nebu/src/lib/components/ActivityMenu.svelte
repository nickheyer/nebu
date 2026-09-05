<script lang="ts">
  import { Popover } from 'bits-ui';
  import { activeTasks, live, clock } from '$lib/state.svelte';
  import { pct, bytes, duration, newestFirst } from '$lib/format';
  import { TaskState } from '$proto/task_pb';
  import { Activity, ChevronRight } from '@lucide/svelte';
  import Spinner from './ui/Spinner.svelte';
  import Progress from './ui/Progress.svelte';
  import StateBadge from './ui/StateBadge.svelte';

  const active = $derived(activeTasks());
  const recent = $derived(
    [...live.tasks.values()]
      .filter((t) => t.state !== TaskState.PENDING && t.state !== TaskState.RUNNING)
      .sort(newestFirst((t) => t.createdAt))
      .slice(0, 5)
  );
</script>

<Popover.Root>
  <Popover.Trigger
    class="flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-sm text-fg-muted transition-colors hover:bg-raised hover:text-fg data-[state=open]:bg-raised data-[state=open]:text-fg"
  >
    {#if active.length}
      <Spinner size={15} class="text-accent" />
    {:else}
      <Activity size={15} />
    {/if}
    <span class="flex-1 text-left">Activity</span>
    {#if active.length}
      <span class="rounded-full bg-accent px-1.5 text-[10.5px] font-semibold text-accent-fg tabular-nums">{active.length}</span>
    {/if}
  </Popover.Trigger>
  <Popover.Portal>
    <Popover.Content side="right" align="end" sideOffset={12} class="enter-up z-[60] w-96 rounded-xl border border-line bg-overlay shadow-pop focus:outline-none">
      <div class="border-b border-line px-4 py-3">
        <div class="text-sm font-semibold text-fg">Activity</div>
        <div class="text-xs text-fg-faint">{active.length ? `${active.length} running` : 'Nothing running'}</div>
      </div>
      <div class="max-h-[60vh] overflow-y-auto">
        {#each active as t (t.id)}
          <a href="/tasks?id={t.id}" class="block border-b border-line/60 px-4 py-3 transition-colors hover:bg-raised/60">
            <div class="flex items-center gap-2">
              <span class="truncate text-sm text-fg">{t.title}</span>
              <span class="ml-auto shrink-0 text-[11px] text-fg-faint tabular-nums">{duration(t.startedAt ?? t.createdAt, undefined, clock.now)}</span>
            </div>
            <Progress done={t.progress?.done} total={t.progress?.total} active class="mt-2" />
            <div class="mt-1 truncate text-[11px] text-fg-muted">
              {#if t.progress?.total}
                {pct(t.progress.done, t.progress.total).toFixed(0)}%{#if t.progress.total >= 100000n}<span> · {bytes(t.progress.done)} of {bytes(t.progress.total)}</span>{/if}
              {/if}
              {#if t.progress?.message}<span> · {t.progress.message}</span>{/if}
            </div>
          </a>
        {/each}
        {#if recent.length}
          <div class="px-4 pt-3 pb-1 text-[11px] font-semibold tracking-wider text-fg-faint uppercase">Recent</div>
          {#each recent as t (t.id)}
            <a href="/tasks?id={t.id}" class="flex items-center gap-2 px-4 py-2 text-sm transition-colors hover:bg-raised/60">
              <StateBadge values={TaskState} value={t.state} size="xs" />
              <span class="truncate text-fg-muted">{t.title}</span>
            </a>
          {/each}
        {/if}
      </div>
      <a href="/tasks" class="flex items-center justify-between border-t border-line px-4 py-2.5 text-xs text-fg-muted hover:text-fg">All tasks <ChevronRight size={13} /></a>
    </Popover.Content>
  </Popover.Portal>
</Popover.Root>
