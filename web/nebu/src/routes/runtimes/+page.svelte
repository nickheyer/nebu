<script lang="ts">
  import { goto } from '$app/navigation';
  import { cached, installsOf, taskActive, formatBlurb } from '$lib/state.svelte';
  import { fit, installTask, taskDoing } from '$lib/runtimes';
  import { Cpu, Download } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Chip from '$lib/components/ui/Chip.svelte';
  import SkeletonRows from '$lib/components/ui/SkeletonRows.svelte';
  import TaskChip from '$lib/components/TaskChip.svelte';

  const runtimes = $derived(cached.runtimes);
  const loading = $derived(!cached.loaded);
</script>

{#snippet head()}
  <thead>
    <tr>
      <th>Runtime</th>
      <th>Formats</th>
      <th>Status</th>
      <th></th>
    </tr>
  </thead>
{/snippet}

<PageHeader title="Runtimes" />

{#if cached.error && runtimes.length === 0}
  <div class="note note-bad">{cached.error}</div>
{:else if loading}
  <table class="tbl">
    {@render head()}
    <tbody><SkeletonRows rows={4} cols={['w-24', 'w-14', 'w-24', { w: 'w-16', num: true }]} /></tbody>
  </table>
{:else if runtimes.length === 0}
  <Empty icon={Cpu} title="No runtimes available" />
{:else}
  <div class="overflow-x-auto">
    <table class="tbl">
      {@render head()}
      <tbody>
        {#each runtimes as s (s.runtime?.id)}
          {#if s.runtime}
            {@const rt = s.runtime}
            {@const installs = installsOf(rt.id)}
            {@const task = installTask(rt.id)}
            {@const installing = !!task && taskActive(task)}
            {@const f = fit(s)}
            <tr class="row-link" onclick={() => goto(`/runtimes/${rt.id}`)}>
              <td class="font-medium whitespace-nowrap text-fg">{rt.name}</td>
              <td>
                <span class="flex flex-wrap gap-1">
                  {#each rt.formats as id (id)}<Chip text={id} title={formatBlurb(id)} />{/each}
                </span>
              </td>
              <td>
                {#if installing && task}
                  <TaskChip {task} label={taskDoing(s, task)} />
                {:else if installs.length}
                  <span class="flex items-center gap-2">
                    <State tone="ok" label="Installed" />
                    <span class="font-mono text-xs text-fg-muted">{installs[0].version}{#if installs.length > 1}<span class="text-fg-faint">{' '}+{installs.length - 1}</span>{/if}</span>
                  </span>
                {:else if s.compatible}
                  <State tone="neutral" label="Not installed" />
                {:else}
                  <State tone={f.tone} label={f.label} />
                {/if}
              </td>
              <td class="actions" onclick={(e) => e.stopPropagation()}>
                <span>
                  {#if s.compatible && installs.length === 0 && !installing}
                    <Button size="sm" icon={Download} href="/runtimes/{rt.id}">Install</Button>
                  {/if}
                </span>
              </td>
            </tr>
          {/if}
        {/each}
      </tbody>
    </table>
  </div>
{/if}
