<script lang="ts">
  import { live, clock, liveInstances, instanceLive, slotName, groupLabel, orderedSlots, answersOf, runtimeName } from '$lib/state.svelte';
  import { launch } from '$lib/launch';
  import { instanceMemory } from '$lib/instances';
  import { ago, newestFirst, tail, when } from '$lib/format';
  import { InstanceState } from '$proto/instance_pb';
  import { SlotState } from '$proto/slot_pb';
  import { Plus, RotateCcw, Compass, Cpu, ArrowRight } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import Segmented from '$lib/components/ui/Segmented.svelte';
  import State from '$lib/components/ui/State.svelte';
  import SlotCard from '$lib/components/SlotCard.svelte';
  import InstanceCard from '$lib/components/InstanceCard.svelte';
  import Connect from '$lib/components/Connect.svelte';
  import Routes from '$lib/components/Routes.svelte';
  import ActiveTasks from '$lib/components/ActiveTasks.svelte';
  import TraceTable from '$lib/components/TraceTable.svelte';

  let historyView = $state('all');

  const loading = $derived(!live.ready && !live.error);
  const slots = $derived(orderedSlots());
  const serving = $derived(slots.filter((s) => s.state === SlotState.READY).length);
  const standalone = $derived(liveInstances().filter((i) => !i.slotId));
  const past = $derived([...live.instances.values()].filter((i) => !instanceLive(i)).sort(newestFirst((i) => i.createdAt)));
  const failed = $derived(past.filter((i) => i.state === InstanceState.FAILED));
  const history = $derived(historyView === 'failed' ? failed : past);
  const recent = $derived(answersOf().slice(0, 8));
</script>

<PageHeader title="Serve">
  {#snippet meta()}
    {#if slots.length}<span>{serving} of {slots.length} slots serving</span>{/if}
  {/snippet}
  <Button variant="primary" icon={Plus} href="/slots/new">New slot</Button>
</PageHeader>

<div class="flex flex-col gap-8">
  <ActiveTasks />

  {#if loading}
    <div class="flex flex-col gap-2" aria-busy="true">
      {#each [0, 1] as i (i)}<div class="skeleton h-16"></div>{/each}
    </div>
  {:else if slots.length === 0 && standalone.length === 0}
    <Empty title={live.installs.size === 0 ? 'Install a runtime to start serving' : live.models.size === 0 ? 'Download a model to start serving' : 'No slots yet'}>
      {#if live.installs.size === 0}
        <Button variant="primary" icon={Cpu} href="/runtimes">Runtimes</Button>
      {:else if live.models.size === 0}
        <Button variant="primary" icon={Compass} href="/catalog">Browse catalog</Button>
      {:else}
        <Button variant="primary" icon={Plus} href="/slots/new">New slot</Button>
      {/if}
    </Empty>
  {:else}
    <div class="flex flex-col gap-2">
      {#each slots as s (s.id)}
        <SlotCard slot={s} />
      {/each}
      {#each standalone as i (i.id)}
        <InstanceCard instance={i} />
      {/each}
    </div>
  {/if}

  <Connect />

  <Routes />

  <Section title="Recent requests">
    {#snippet actions()}
      <Button size="sm" variant="ghost" icon={ArrowRight} href="/requests">All requests</Button>
    {/snippet}
    <TraceTable traces={recent} compact onSelect={(t) => (window.location.href = `/requests?id=${t.id}`)} />
  </Section>

  {#if past.length}
    <Section title="History" count={past.length}>
      {#snippet actions()}
        <Segmented size="sm" bind:value={historyView} tabs={[{ id: 'all', label: 'All' }, { id: 'failed', label: 'Failed', count: failed.length || undefined }]} />
      {/snippet}
      {#if history.length === 0}
        <Empty compact title="No failures" />
      {:else}
        <div class="overflow-x-auto">
          <table class="tbl">
            <thead><tr><th>Instance</th><th>Model</th><th>State</th><th>Runtime</th><th class="num">Memory</th><th>Slot</th><th>Ended</th><th></th></tr></thead>
            <tbody>
              {#each history as i (i.id)}
                <tr class="row-link" onclick={() => (window.location.href = `/instances/${i.id}`)}>
                  <td class="font-mono text-xs text-fg">{i.name}</td>
                  <td>
                    <div class="truncate text-fg" title={i.repo}>{tail(i.repo)} <span class="font-mono text-xs text-fg-muted">{groupLabel(i)}</span></div>
                    {#if i.triage[0]?.summary || (i.state === InstanceState.FAILED && i.error)}
                      <div class="max-w-md truncate text-xs text-bad" title={i.triage[0]?.summary || i.error}>{i.triage[0]?.summary || i.error}</div>
                    {/if}
                  </td>
                  <td><State values={InstanceState} value={i.state} /></td>
                  <td class="text-fg-muted">{runtimeName(i.runtimeId)}</td>
                  <td class="num text-fg-muted">{instanceMemory(i) || '–'}</td>
                  <td class="font-mono text-xs text-fg-muted">{i.slotId ? slotName(i.slotId) : '–'}</td>
                  <td class="text-fg-muted whitespace-nowrap" title={when(i.stoppedAt ?? i.createdAt)}>{ago(i.stoppedAt ?? i.createdAt, clock.now)}</td>
                  <td class="actions" onclick={(e) => e.stopPropagation()}>
                    <span>{#if i.request}<IconButton size="sm" icon={RotateCcw} label="Run again" onclick={() => launch({ ...i.request! })} />{/if}</span>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </Section>
  {/if}
</div>
