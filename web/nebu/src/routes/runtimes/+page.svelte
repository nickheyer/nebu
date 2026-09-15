<script lang="ts">
  import { goto } from '$app/navigation';
  import { cached, installsOf } from '$lib/state.svelte';
  import { apiName, runtimeTasks } from '$lib/runtimes';
  import { Cpu } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Tip from '$lib/components/ui/Tip.svelte';
  import SkeletonRows from '$lib/components/ui/SkeletonRows.svelte';
  import TaskChip from '$lib/components/TaskChip.svelte';

  const runtimes = $derived(cached.runtimes.flatMap((status) => (status.runtime ? [{ status, rt: status.runtime }] : [])));
</script>

{#snippet head()}
  <thead><tr><th>Runtime</th><th>Formats</th><th>API</th><th class="num">Installs</th><th>Host</th></tr></thead>
{/snippet}

<PageHeader title="Runtimes" />

<div class="overflow-x-auto">
  {#if !cached.loaded}
    <table class="tbl">
      {@render head()}
      <tbody><SkeletonRows rows={4} cols={[{ w: 'w-40', sub: true }, 'w-24', 'w-16', { w: 'w-8', num: true }, 'w-24']} /></tbody>
    </table>
  {:else if !runtimes.length}
    {#if cached.error}<div class="note note-bad">{cached.error}</div>{:else}<Empty icon={Cpu} title="No runtimes" />{/if}
  {:else}
    <table class="tbl">
      {@render head()}
      <tbody>
        {#each runtimes as { status, rt } (rt.id)}
          {@const installs = installsOf(rt.id)}
          {@const task = runtimeTasks(rt.id)[0]}
          <tr class="row-link" onclick={() => goto(`/runtimes/${rt.id}`)}>
            <td>
              <div class="font-medium text-fg">{rt.name}</div>
              <div class="max-w-md truncate text-xs text-fg-faint" title={rt.description}>{rt.description}</div>
            </td>
            <td class="font-mono text-xs text-fg-muted">{rt.formats.join(' ')}</td>
            <td class="text-fg-muted">{apiName[rt.api]}</td>
            <td class="num text-fg-muted">{installs.length || '–'}</td>
            <td>
              {#if task}
                <TaskChip {task} />
              {:else if !status.compatible}
                <Tip text={status.unmet.join(' · ')}><State tone="bad" label="Incompatible" /></Tip>
              {:else if installs.length}
                <State tone="ok" label="Installed" />
              {:else}
                <State tone="neutral" label="Not installed" />
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>
