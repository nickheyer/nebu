<script lang="ts">
  import { live, clock, liveInstances, instanceLive, slotName, groupLabel } from '$lib/state.svelte';
  import { selectionParam } from '$lib/selection.svelte';
  import { launch } from '$lib/launch';
  import { instanceMemory } from '$lib/instances';
  import { ago, byName, newestFirst, tail, when } from '$lib/format';
  import { InstanceState } from '$proto/instance_pb';
  import { SlotState, type Slot } from '$proto/slot_pb';
  import { Plus, RotateCcw, Compass, Cpu } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import Segmented from '$lib/components/ui/Segmented.svelte';
  import State from '$lib/components/ui/State.svelte';
  import SlotRow from '$lib/components/SlotRow.svelte';
  import InstanceRow from '$lib/components/InstanceRow.svelte';
  import SlotDrawer from '$lib/components/SlotDrawer.svelte';
  import InstanceDrawer from '$lib/components/InstanceDrawer.svelte';
  import Connect from '$lib/components/Connect.svelte';
  import Routes from '$lib/components/Routes.svelte';
  import Guide from '$lib/components/Guide.svelte';
  import ActiveTasks from '$lib/components/ActiveTasks.svelte';

  const slotSel = selectionParam('/', 'slot');
  const instSel = selectionParam('/', 'instance');
  let slotTab = $state('overview');
  let historyView = $state('all');

  const loading = $derived(!live.ready && !live.error);
  const slots = $derived([...live.slots.values()].sort(byName((s) => s.name)));
  const serving = $derived(slots.filter((s) => s.state === SlotState.READY).length);
  const standalone = $derived(liveInstances().filter((i) => !i.slotId));
  const past = $derived([...live.instances.values()].filter((i) => !instanceLive(i)).sort(newestFirst((i) => i.createdAt)));
  const failed = $derived(past.filter((i) => i.state === InstanceState.FAILED));
  const history = $derived(historyView === 'failed' ? failed : past);

  function openSlot(s: Slot, tab = 'overview') {
    instSel.id = '';
    slotTab = tab;
    slotSel.id = s.id;
  }
  function newSlot() {
    instSel.id = '';
    slotTab = 'settings';
    slotSel.id = 'new';
  }
  function openInstance(id: string) {
    slotSel.id = '';
    instSel.id = id;
  }
</script>

<PageHeader title="Serve" />

<div class="flex flex-col gap-9">
  <Guide />
  <ActiveTasks />

  <Section title="Slots" meta={slots.length ? `${serving} of ${slots.length} serving` : ''}>
    {#snippet actions()}
      <Button size="sm" icon={Plus} onclick={newSlot}>New slot</Button>
    {/snippet}
    {#if loading}
      <div class="flex flex-col gap-1.5" aria-busy="true">
        {#each [0, 1] as i (i)}<div class="skeleton h-16"></div>{/each}
      </div>
    {:else if slots.length === 0 && standalone.length === 0}
      <Empty title={live.installs.size === 0 ? 'No runtime installed' : live.models.size === 0 ? 'Nothing in the library' : 'No slots'}>
        {#if live.installs.size === 0}
          <Button size="sm" variant="primary" icon={Cpu} href="/runtimes">Runtimes</Button>
        {:else if live.models.size === 0}
          <Button size="sm" variant="primary" icon={Compass} href="/catalog">Discover models</Button>
        {:else}
          <Button size="sm" variant="primary" icon={Plus} onclick={newSlot}>New slot</Button>
        {/if}
      </Empty>
    {:else}
      <div class="flex flex-col gap-1.5">
        {#each slots as s (s.id)}
          <SlotRow slot={s} onOpen={openSlot} />
        {/each}
        {#each standalone as i (i.id)}
          <InstanceRow instance={i} onOpen={(x) => openInstance(x.id)} />
        {/each}
      </div>
    {/if}
  </Section>

  <Connect />

  <Routes />

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
                <tr class="row-link {instSel.id === i.id ? 'row-active' : ''}" onclick={() => openInstance(i.id)}>
                  <td class="font-mono text-xs text-fg">{i.name}</td>
                  <td>
                    <div class="truncate text-fg" title={i.repo}>{tail(i.repo)} <span class="font-mono text-xs text-fg-muted">{groupLabel(i)}</span></div>
                    {#if i.triage[0]?.summary || (i.state === InstanceState.FAILED && i.error)}
                      <div class="max-w-md truncate text-xs text-bad" title={i.triage[0]?.summary || i.error}>{i.triage[0]?.summary || i.error}</div>
                    {/if}
                  </td>
                  <td><State values={InstanceState} value={i.state} /></td>
                  <td class="text-fg-muted">{i.runtimeId}</td>
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

<SlotDrawer bind:id={slotSel.id} bind:tab={slotTab} onInstance={openInstance} />
<InstanceDrawer bind:id={instSel.id} />
