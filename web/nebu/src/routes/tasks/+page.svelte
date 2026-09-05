<script lang="ts">
  import { api } from '$lib/api';
  import { live, clock, taskActive } from '$lib/state.svelte';
  import { selectionParam } from '$lib/selection.svelte';
  import { ago, duration, newestFirst, when, pct, bytes } from '$lib/format';
  import { fail } from '$lib/toast.svelte';
  import { TaskState, type Task } from '$proto/task_pb';
  import { ListChecks, Download, Hammer, ArrowLeftRight, Play, ShieldCheck, FolderOutput, Radar, Package, Ban } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Segmented from '$lib/components/ui/Segmented.svelte';
  import StatePill from '$lib/components/ui/StatePill.svelte';
  import Progress from '$lib/components/ui/Progress.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import SkeletonRows from '$lib/components/ui/SkeletonRows.svelte';
  import TaskDrawer from '$lib/components/TaskDrawer.svelte';

  let view = $state('all');
  const sel = selectionParam('/tasks');

  const loading = $derived(!live.ready && !live.error);
  const all = $derived([...live.tasks.values()].sort(newestFirst((t) => t.createdAt)));
  const active = $derived(all.filter(taskActive));
  const failed = $derived(all.filter((t) => t.state === TaskState.FAILED));
  const list = $derived(view === 'active' ? active : view === 'failed' ? failed : all);

  const icons: Record<string, any> = { pull: Download, build: Hammer, swap: ArrowLeftRight, run: Play, verify: ShieldCheck, export: FolderOutput, check: Radar, install: Package };

  async function cancel(t: Task) {
    try {
      await api.tasks.cancelTask({ id: t.id });
    } catch (err) {
      fail(err, 'Cancel failed');
    }
  }
</script>

{#snippet head()}
  <thead><tr><th class="w-10"></th><th>Task</th><th>State</th><th class="w-72">Progress</th><th class="num">Took</th><th class="num">Started</th><th></th></tr></thead>
{/snippet}

<PageHeader title="Tasks">
  <Segmented bind:value={view} tabs={[{ id: 'all', label: 'All', count: all.length }, { id: 'active', label: 'Active', count: active.length }, { id: 'failed', label: 'Failed', count: failed.length || undefined }]} />
</PageHeader>

<Card flush>
  {#if loading}
    <table class="tbl">
      {@render head()}
      <tbody><SkeletonRows rows={4} cols={['w-4', { w: 'w-48', sub: true }, 'w-16', 'w-40', { w: 'w-10', num: true }, { w: 'w-12', num: true }, { w: 'w-6', num: true }]} /></tbody>
    </table>
  {:else if list.length === 0}
    <Empty icon={ListChecks} title={view === 'active' ? 'Nothing running' : view === 'failed' ? 'No failures' : 'No tasks yet'} />
  {:else}
    <div class="overflow-x-auto">
      <table class="tbl">
        {@render head()}
        <tbody>
          {#each list as t (t.id)}
            {@const Icon = icons[t.kind] ?? ListChecks}
            {@const running = taskActive(t)}
            <tr class="row-link {sel.id === t.id ? 'row-active' : ''}" onclick={() => (sel.id = t.id)}>
              <td class="text-fg-faint"><Icon size={16} /></td>
              <td>
                <div class="text-fg">{t.title}</div>
                <div class="flex items-center gap-2 text-xs text-fg-faint">
                  <span class="font-mono">{t.kind}</span>
                  {#if t.error}<span class="max-w-md truncate text-bad" title={t.error}>{t.error}</span>{/if}
                </div>
              </td>
              <td><StatePill values={TaskState} value={t.state} /></td>
              <td>
                {#if running || t.progress?.total}
                  <Progress done={t.progress?.done} total={t.progress?.total} active={running} tone={t.state === TaskState.FAILED ? 'bad' : t.state === TaskState.SUCCEEDED ? 'ok' : 'accent'} />
                  <div class="mt-1.5 truncate text-xs tabular-nums text-fg-muted">
                    {#if t.progress?.total}
                      {pct(t.progress.done, t.progress.total).toFixed(0)}%{#if t.progress.total >= 100000n}<span>{' · '}{bytes(t.progress.done)} of {bytes(t.progress.total)}</span>{/if}
                    {/if}
                    {#if t.progress?.message}<span>{t.progress?.total ? ' · ' : ''}{t.progress.message}</span>{/if}
                  </div>
                {:else}
                  <span class="text-xs text-fg-faint">{t.progress?.message ?? ''}</span>
                {/if}
              </td>
              <td class="num text-fg-muted">{t.startedAt ? duration(t.startedAt, t.finishedAt, clock.now) : '–'}</td>
              <td class="num text-fg-muted" title={when(t.createdAt)}>{ago(t.createdAt, clock.now)}</td>
              <td class="actions" onclick={(e) => e.stopPropagation()}>
                {#if running}<Button size="sm" variant="ghost" icon={Ban} aria-label="Cancel {t.title}" onclick={() => cancel(t)} />{/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</Card>

<TaskDrawer bind:id={sel.id} />
