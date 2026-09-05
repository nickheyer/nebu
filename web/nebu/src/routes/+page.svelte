<script lang="ts">
  import { page } from '$app/state';
  import { afterNavigate, replaceState } from '$app/navigation';
  import { baseUrl } from '$lib/api';
  import { live, cached, clock, liveInstances, instanceLive, unackedFindings, slotName, probeHost } from '$lib/state.svelte';
  import { newSlot, runModel } from '$lib/slotActions.svelte';
  import { selectionParam } from '$lib/selection.svelte';
  import { listenerUrl } from '$lib/gateway';
  import { launch } from '$lib/launch';
  import { instanceMemory } from '$lib/instances';
  import { ago, byName, newestFirst, plural, tail, when } from '$lib/format';
  import { InstanceState } from '$proto/instance_pb';
  import { SlotState } from '$proto/slot_pb';
  import { Plus, Play, Plug, Radar, ArrowRight, RotateCcw, RefreshCw, Compass, Cpu } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Copy from '$lib/components/ui/Copy.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import Segmented from '$lib/components/ui/Segmented.svelte';
  import State from '$lib/components/ui/State.svelte';
  import MemoryBars from '$lib/components/MemoryBars.svelte';
  import SlotRow from '$lib/components/SlotRow.svelte';
  import InstanceRow from '$lib/components/InstanceRow.svelte';
  import SlotDrawer from '$lib/components/SlotDrawer.svelte';
  import InstanceDrawer from '$lib/components/InstanceDrawer.svelte';
  import ConnectDialog from '$lib/components/ConnectDialog.svelte';
  import Guide from '$lib/components/Guide.svelte';
  import ActiveTasks from '$lib/components/ActiveTasks.svelte';

  const slotSel = selectionParam('/', 'slot');
  const instSel = selectionParam('/', 'instance');
  let connectOpen = $state(false);
  let historyView = $state('all');
  let probing = $state(false);

  const loading = $derived(!live.ready && !live.error);
  const slots = $derived([...live.slots.values()].sort(byName((s) => s.name)));
  const serving = $derived(slots.filter((s) => s.state === SlotState.READY).length);
  const standalone = $derived(liveInstances().filter((i) => !i.slotId));
  const past = $derived([...live.instances.values()].filter((i) => !instanceLive(i)).sort(newestFirst((i) => i.createdAt)));
  const failed = $derived(past.filter((i) => i.state === InstanceState.FAILED));
  const history = $derived(historyView === 'failed' ? failed : past);
  const findings = $derived(unackedFindings());
  // The gateway's own listener when it has one, else the API listener this page came from
  const endpoint = $derived.by(() => {
    const own = cached.gateway?.listeners.find((l) => !l.shared);
    return (own ? listenerUrl(own.addr, !!cached.gateway?.tls) : baseUrl) + '/v1';
  });

  // The old gateway page's link opens the connect dialog here, the flag dropped once read
  afterNavigate(() => {
    if (page.url.searchParams.get('connect') === '1') {
      connectOpen = true;
      replaceState('/', {});
    }
  });

  function openInstance(id: string) {
    slotSel.id = '';
    instSel.id = id;
  }

  async function probe() {
    probing = true;
    await probeHost();
    probing = false;
  }
</script>

<PageHeader title="Serve">
  {#snippet meta()}
    <span class="inline-flex items-center gap-0.5 font-mono text-fg">{endpoint}<Copy text={endpoint} size={13} /></span>
  {/snippet}
  <Button icon={Plug} onclick={() => (connectOpen = true)}>Connect</Button>
  <Button variant="primary" icon={Play} onclick={() => runModel(null)} disabled={live.models.size === 0}>Run a model</Button>
</PageHeader>

<div class="flex flex-col gap-9">
  <Guide />

  {#if findings.length}
    <a href="/monitor" class="note note-info flex items-center gap-3">
      <Radar size={15} />
      <span class="flex-1">{plural(findings.length, 'new finding')}</span>
      <ArrowRight size={15} />
    </a>
  {/if}

  <ActiveTasks />

  <Section title="Memory" meta={live.host ? `probed ${ago(live.host.probedAt, clock.now)}` : ''}>
    {#snippet actions()}
      <IconButton size="sm" icon={RefreshCw} label="Probe host" loading={probing} onclick={probe} />
    {/snippet}
    {#if live.host}
      <MemoryBars host={live.host} />
    {:else}
      <div class="flex flex-col gap-2" aria-busy="true">{#each [0, 1] as i (i)}<div class="skeleton h-5"></div>{/each}</div>
    {/if}
  </Section>

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
          <Button size="sm" icon={Play} onclick={() => runModel(null)}>Run a model</Button>
        {/if}
      </Empty>
    {:else}
      <div class="flex flex-col gap-1.5">
        {#each slots as s (s.id)}
          <SlotRow slot={s} onOpen={(x) => (slotSel.id = x.id)} />
        {/each}
        {#each standalone as i (i.id)}
          <InstanceRow instance={i} onOpen={(x) => openInstance(x.id)} />
        {/each}
      </div>
    {/if}
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
                <tr class="row-link {instSel.id === i.id ? 'row-active' : ''}" onclick={() => openInstance(i.id)}>
                  <td class="font-mono text-xs text-fg">{i.name}</td>
                  <td>
                    <div class="truncate text-fg" title={i.repo}>{tail(i.repo)} <span class="font-mono text-xs text-fg-muted">{i.group}</span></div>
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

<SlotDrawer bind:id={slotSel.id} onInstance={openInstance} />
<InstanceDrawer bind:id={instSel.id} />
<ConnectDialog bind:open={connectOpen} />
