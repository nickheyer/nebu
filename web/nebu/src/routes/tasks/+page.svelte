<script lang="ts">
  import { live, clock, taskActive } from '$lib/state.svelte';
  import { selectionParam } from '$lib/selection.svelte';
  import { ago, duration, newestFirst, when, pct, bytes } from '$lib/format';
  import { TaskState } from '$proto/task_pb';
  import { ListChecks, Download, Hammer, ArrowLeftRight, Play, ShieldCheck, FolderOutput, Radar, Package } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Panel from '$lib/components/ui/Panel.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Tabs from '$lib/components/ui/Tabs.svelte';
  import StateBadge from '$lib/components/ui/StateBadge.svelte';
  import Progress from '$lib/components/ui/Progress.svelte';
  import TaskDrawer from '$lib/components/TaskDrawer.svelte';

  let view = $state('all');
  const sel = selectionParam('/tasks');

  const all = $derived([...live.tasks.values()].sort(newestFirst((t) => t.createdAt)));
  const active = $derived(all.filter(taskActive));
  const failed = $derived(all.filter((t) => t.state === TaskState.FAILED));
  const list = $derived(view === 'active' ? active : view === 'failed' ? failed : all);

  const icons: Record<string, any> = { pull: Download, build: Hammer, swap: ArrowLeftRight, run: Play, verify: ShieldCheck, export: FolderOutput, check: Radar, install: Package };
</script>

<PageHeader title="Tasks">
  <Tabs bind:value={view} tabs={[{ id: 'all', label: 'All', count: all.length }, { id: 'active', label: 'Active', count: active.length }, { id: 'failed', label: 'Failed', count: failed.length || undefined }]} />
</PageHeader>

<Panel flush>
  {#if list.length === 0}
    <Empty icon={ListChecks} title={view === 'active' ? 'Nothing running' : view === 'failed' ? 'No failures' : 'No tasks'} />
  {:else}
    <div class="overflow-x-auto">
      <table class="tbl">
        <thead><tr><th class="w-8"></th><th>task</th><th>state</th><th class="w-64">progress</th><th class="num">took</th><th class="num">started</th></tr></thead>
        <tbody>
          {#each list as t (t.id)}
            {@const Icon = icons[t.kind] ?? ListChecks}
            {@const running = taskActive(t)}
            <tr class="row-link {sel.id === t.id ? 'row-active' : ''}" onclick={() => (sel.id = t.id)}>
              <td class="text-fg-faint"><Icon size={15} /></td>
              <td>
                <div class="text-sm text-fg">{t.title}</div>
                <div class="flex items-center gap-2 text-[11px] text-fg-faint">
                  <span class="font-mono">{t.kind}</span>
                  {#if t.error}<span class="max-w-md truncate text-bad" title={t.error}>{t.error}</span>{/if}
                </div>
              </td>
              <td><StateBadge values={TaskState} value={t.state} size="xs" /></td>
              <td>
                {#if running || t.progress?.total}
                  <Progress done={t.progress?.done} total={t.progress?.total} active={running} tone={t.state === TaskState.FAILED ? 'bad' : t.state === TaskState.SUCCEEDED ? 'ok' : 'accent'} />
                  <div class="mt-1 truncate text-[11px] text-fg-muted tabular-nums">
                    {#if t.progress?.total}
                      {pct(t.progress.done, t.progress.total).toFixed(0)}%{#if t.progress.total >= 100000n}<span> · {bytes(t.progress.done)} of {bytes(t.progress.total)}</span>{/if}
                    {/if}
                    {#if t.progress?.message}<span> · {t.progress.message}</span>{/if}
                  </div>
                {:else}
                  <span class="text-xs text-fg-faint">{t.progress?.message ?? ''}</span>
                {/if}
              </td>
              <td class="num text-xs text-fg-muted">{t.startedAt ? duration(t.startedAt, t.finishedAt, clock.now) : '–'}</td>
              <td class="num text-xs text-fg-muted" title={when(t.createdAt)}>{ago(t.createdAt, clock.now)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</Panel>

<TaskDrawer bind:id={sel.id} />
