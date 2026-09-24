<script lang="ts">
  import { goto } from '$app/navigation';
  import { api } from '$lib/api';
  import { live, clock, taskActive } from '$lib/state.svelte';
  import { ago, duration, newestFirst, when, pct, bytes } from '$lib/format';
  import { fail } from '$lib/toast.svelte';
  import { TaskState, type Task } from '$proto/task_pb';
  import { ListChecks, Ban } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Tabs from '$lib/components/ui/Tabs.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Progress from '$lib/components/ui/Progress.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import SkeletonRows from '$lib/components/ui/SkeletonRows.svelte';


  let view = $state('all');

  const loading = $derived(!live.ready && !live.error);
  const all = $derived([...live.tasks.values()].sort(newestFirst((t) => t.createdAt)));
  const active = $derived(all.filter(taskActive));
  const failed = $derived(all.filter((t) => t.state === TaskState.FAILED));
  const list = $derived(view === 'active' ? active : view === 'failed' ? failed : all);

  async function cancel(t: Task) {
    try {
      await api.tasks.cancelTask({ id: t.id });
    } catch (err) {
      fail(err, 'Cancel failed');
    }
  }
</script>

{#snippet head()}
  <thead><tr><th>Task</th><th>Kind</th><th>State</th><th class="w-72">Progress</th><th class="num">Took</th><th class="num">Started</th><th></th></tr></thead>
{/snippet}

<PageHeader title="Tasks">
  {#snippet below()}
    <Tabs bind:value={view} tabs={[{ id: 'all', label: 'All', count: all.length }, { id: 'active', label: 'Running', count: active.length }, { id: 'failed', label: 'Failed', count: failed.length || undefined }]} />
  {/snippet}
</PageHeader>

<div class="tbl-wrap">
    {#if loading}
      <table class="tbl">
        {@render head()}
        <tbody><SkeletonRows rows={4} cols={[{ w: 'w-48', sub: true }, 'w-12', 'w-16', 'w-40', { w: 'w-10', num: true }, { w: 'w-12', num: true }, { w: 'w-6', num: true }]} /></tbody>
      </table>
    {:else if list.length === 0}
      <Empty compact icon={ListChecks} title={view === 'active' ? 'Nothing is running' : view === 'failed' ? 'No failed tasks' : 'No tasks yet'} />
    {:else}
      <table class="tbl">
        {@render head()}
        <tbody>
          {#each list as t (t.id)}
            {@const running = taskActive(t)}
            <tr class="row-link" onclick={() => goto(`/tasks/${t.id}`)}>
              <td>
                <div class="text-fg">{t.title}</div>
                {#if t.error}<div class="max-w-md truncate text-xs text-bad" title={t.error}>{t.error}</div>{/if}
              </td>
              <td class="font-mono text-xs text-fg-muted">{t.kind}</td>
              <td><State values={TaskState} value={t.state} /></td>
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
                <span>{#if running}<IconButton size="xs" icon={Ban} label="Cancel" onclick={() => cancel(t)} />{/if}</span>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
</div>
