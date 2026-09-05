<script lang="ts">
  import { page } from '$app/state';
  import { afterNavigate, replaceState } from '$app/navigation';
  import { baseUrl } from '$lib/api';
  import { live, cached, clock, liveInstances, instanceLive, unackedFindings, slotName } from '$lib/state.svelte';
  import { newSlot, runModel } from '$lib/slotActions.svelte';
  import { selectionParam } from '$lib/selection.svelte';
  import { listenerUrl } from '$lib/gateway';
  import { launch } from '$lib/launch';
  import { instanceMemory, shortRepo } from '$lib/instances';
  import { ago, byName, newestFirst, when } from '$lib/format';
  import { InstanceState, type Instance } from '$proto/instance_pb';
  import { SlotState } from '$proto/slot_pb';
  import { Plus, Play, Plug, Radar, ArrowRight, LayoutGrid, RotateCcw } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Copy from '$lib/components/ui/Copy.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Segmented from '$lib/components/ui/Segmented.svelte';
  import StatePill from '$lib/components/ui/StatePill.svelte';
  import SkeletonRows from '$lib/components/ui/SkeletonRows.svelte';
  import MemoryStrip from '$lib/components/MemoryStrip.svelte';
  import SlotCard from '$lib/components/SlotCard.svelte';
  import InstanceCard from '$lib/components/InstanceCard.svelte';
  import SlotDrawer from '$lib/components/SlotDrawer.svelte';
  import InstanceDrawer from '$lib/components/InstanceDrawer.svelte';
  import ConnectDialog from '$lib/components/ConnectDialog.svelte';
  import Setup from '$lib/components/Setup.svelte';
  import ActiveTasks from '$lib/components/ActiveTasks.svelte';

  const slotSel = selectionParam('/', 'slot');
  const instSel = selectionParam('/', 'instance');
  let connectOpen = $state(false);
  let historyView = $state('all');

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
</script>

<PageHeader title="Serve">
  {#snippet meta()}
    <span class="inline-flex items-center gap-1 font-mono text-fg">{endpoint}<Copy text={endpoint} size={14} class="h-7 w-7" /></span>
    {#if !loading}<span>{slots.length ? `${serving} of ${slots.length} slots serving` : 'No slots yet'}</span>{/if}
  {/snippet}
  <Button icon={Plug} onclick={() => (connectOpen = true)}>Connect</Button>
  <Button icon={Plus} onclick={newSlot}>New slot</Button>
  <Button variant="primary" icon={Play} onclick={() => runModel(null)} disabled={live.models.size === 0}>Run a model</Button>
</PageHeader>

<div class="flex flex-col gap-8">
  <Setup />

  {#if findings.length}
    <a href="/monitor" class="note note-info flex items-center gap-3">
      <Radar size={16} />
      <span class="flex-1">{findings.length} new {findings.length === 1 ? 'finding' : 'findings'} from what you watch</span>
      <ArrowRight size={16} />
    </a>
  {/if}

  <ActiveTasks />

  <section>
    <div class="mb-3 flex items-baseline gap-3">
      <h2 class="section-title">Memory</h2>
      {#if live.host}<span class="text-sm text-fg-faint">probed {ago(live.host.probedAt, clock.now)}</span>{/if}
      <a href="/host" class="ml-auto text-sm text-fg-muted hover:text-fg">Host details</a>
    </div>
    {#if live.host}
      <MemoryStrip host={live.host} />
    {:else}
      <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">{#each [0, 1] as i (i)}<div class="card h-[5.5rem]" aria-busy="true"></div>{/each}</div>
    {/if}
  </section>

  <section>
    <h2 class="section-title mb-3">Slots</h2>
    {#if loading}
      <div class="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
        {#each [0, 1] as i (i)}<div class="card h-52" aria-busy="true"></div>{/each}
      </div>
    {:else if slots.length === 0 && standalone.length === 0}
      <div class="card">
        <Empty icon={LayoutGrid} title="No slots yet" description="A slot is a model name clients send. Create one for each name you want to serve, with the devices and memory it may use.">
          <Button variant="primary" icon={Plus} onclick={newSlot}>Create a slot</Button>
        </Empty>
      </div>
    {:else}
      <div class="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
        {#each slots as s (s.id)}
          <SlotCard slot={s} onOpen={(x) => (slotSel.id = x.id)} />
        {/each}
        {#each standalone as i (i.id)}
          <InstanceCard instance={i} onOpen={(x) => openInstance(x.id)} />
        {/each}
        <button type="button" class="flex min-h-40 items-center justify-center gap-2 rounded-xl border border-dashed border-line text-sm text-fg-muted transition-colors hover:border-line-strong hover:text-fg" onclick={newSlot}>
          <Plus size={16} /> New slot
        </button>
      </div>
    {/if}
  </section>

  {#if past.length}
    <Card title="History" description="Instances that stopped or failed" flush>
      {#snippet actions()}
        <Segmented size="sm" bind:value={historyView} tabs={[{ id: 'all', label: 'All', count: past.length }, { id: 'failed', label: 'Failed', count: failed.length || undefined }]} />
      {/snippet}
      {#if history.length === 0}
        <Empty compact title="No failures" />
      {:else}
        <div class="overflow-x-auto">
          <table class="tbl">
            <thead><tr><th>Instance</th><th>State</th><th>Runtime</th><th class="num">Memory</th><th>Slot</th><th>Ended</th><th></th></tr></thead>
            <tbody>
              {#each history as i (i.id)}
                <tr class="row-link {instSel.id === i.id ? 'row-active' : ''}" onclick={() => openInstance(i.id)}>
                  <td>
                    <div class="font-medium text-fg">{i.name}</div>
                    <div class="truncate font-mono text-xs text-fg-muted" title="{i.repo} {i.group}">{shortRepo(i.repo)} · {i.group}</div>
                    {#if i.triage[0]?.summary || (i.state === InstanceState.FAILED && i.error)}
                      <div class="max-w-md truncate text-xs text-bad" title={i.triage[0]?.summary || i.error}>{i.triage[0]?.summary || i.error}</div>
                    {/if}
                  </td>
                  <td><StatePill values={InstanceState} value={i.state} /></td>
                  <td class="text-fg-muted">{i.runtimeId}</td>
                  <td class="num text-fg-muted">{instanceMemory(i) || '–'}</td>
                  <td class="text-fg-muted">{i.slotId ? slotName(i.slotId) : '–'}</td>
                  <td class="text-fg-muted" title={when(i.stoppedAt ?? i.createdAt)}>{ago(i.stoppedAt ?? i.createdAt, clock.now)}</td>
                  <td class="actions" onclick={(e) => e.stopPropagation()}>
                    {#if i.request}<Button size="sm" variant="ghost" icon={RotateCcw} onclick={() => launch({ ...i.request! })}>Run again</Button>{/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </Card>
  {/if}
</div>

<SlotDrawer bind:id={slotSel.id} onInstance={openInstance} />
<InstanceDrawer bind:id={instSel.id} />
<ConnectDialog bind:open={connectOpen} />
