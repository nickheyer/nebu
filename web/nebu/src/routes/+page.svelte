<script lang="ts">
  import { goto } from '$app/navigation';
  import { live, clock, liveInstances, instanceLive, slotName, groupLabel, orderedSlots, answersOf, runtimeName, taskFor } from '$lib/state.svelte';
  import { launch, slotOccupied } from '$lib/launch';
  import { instanceMemory } from '$lib/instances';
  import { swapSlot, evictSlot, deleteSlot, relaunchSlot, stopInstance } from '$lib/actions.svelte';
  import { ago, count, duration, newestFirst, tail, when } from '$lib/format';
  import { InstanceState, type Instance } from '$proto/instance_pb';
  import { SlotState, type Slot } from '$proto/slot_pb';
  import { RouteState } from '$proto/gateway_pb';
  import { Plus, RotateCcw, Compass, Cpu, ArrowRight, ArrowLeftRight, LogOut, MessageSquare, Play, Settings2, Square, Trash2 } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import Segmented from '$lib/components/ui/Segmented.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import TaskChip from '$lib/components/TaskChip.svelte';
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
  // What the slots table says when it has nothing to list: the first thing missing on the way to serving
  const firstStep = $derived(live.installs.size === 0 ? 'runtime' : live.models.size === 0 ? 'model' : 'slot');

  function instanceOf(s: Slot): Instance | undefined {
    return s.instanceId ? live.instances.get(s.instanceId) : undefined;
  }
  async function stop(i: Instance) {
    await stopInstance(i.id, i.name);
  }
</script>

<PageHeader title="Serve">
  {#snippet meta()}
    {#if slots.length}<span>{serving} of {slots.length} slots serving</span>{/if}
  {/snippet}
  <Button variant="primary" icon={Plus} href="/slots/new">New slot</Button>
</PageHeader>

<div class="flex flex-col gap-8">
  <ActiveTasks />

  <Section title="Slots" count={slots.length || undefined}>
    <div class="overflow-x-auto">
      <table class="tbl">
        <thead><tr><th class="w-8">#</th><th>Name</th><th>Model</th><th>Runtime</th><th>State</th><th class="num">Requests</th><th class="num">Memory</th><th></th></tr></thead>
        <tbody>
          {#if loading}
            {#each [0, 1] as i (i)}
              <tr aria-busy="true"><td colspan="8"><div class="skeleton h-4"></div></td></tr>
            {/each}
          {:else if slots.length === 0 && standalone.length === 0}
            <tr>
              <td colspan="8" class="!p-0">
                <Empty compact class="border-0" title={firstStep === 'runtime' ? 'Install a runtime to start serving' : firstStep === 'model' ? 'Download a model to start serving' : 'No slots yet'}>
                  {#if firstStep === 'runtime'}
                    <Button size="sm" variant="primary" icon={Cpu} href="/runtimes">Runtimes</Button>
                  {:else if firstStep === 'model'}
                    <Button size="sm" variant="primary" icon={Compass} href="/catalog">Browse catalog</Button>
                  {:else}
                    <Button size="sm" variant="primary" icon={Plus} href="/slots/new">New slot</Button>
                  {/if}
                </Empty>
              </td>
            </tr>
          {:else}
            {#each slots as s (s.id)}
              {@const instance = instanceOf(s)}
              {@const route = live.routes.get(s.name)}
              {@const occupied = slotOccupied(s.id)}
              {@const answering = route?.state === RouteState.READY}
              {@const swapTask = taskFor('swap', { slot: s.id })}
              {@const failedSlot = s.state === SlotState.FAILED}
              <tr class="row-link" onclick={() => goto(`/slots/${s.id}`)}>
                <td class="font-mono text-xs text-fg-faint">{s.position}</td>
                <td class="font-mono text-xs text-fg">{s.name}</td>
                <td>
                  {#if s.request?.repo}
                    <div class="truncate text-fg" title={s.request.repo}>{tail(s.request.repo)} <span class="font-mono text-xs text-fg-muted">{groupLabel(s.request)}</span></div>
                  {:else}
                    <span class="text-fg-faint">Empty</span>
                  {/if}
                  {#if swapTask}<div class="mt-1"><TaskChip task={swapTask} label="Swapping" /></div>{/if}
                  {#if failedSlot && s.error}<div class="max-w-md truncate text-xs text-bad" title={s.error}>{s.error}</div>{/if}
                </td>
                <td class="text-fg-muted">{instance ? runtimeName(instance.runtimeId) : s.runtimeId ? runtimeName(s.runtimeId) : '–'}</td>
                <td>
                  <State values={SlotState} value={s.state} />
                  {#if occupied && instance}<span class="ml-2 text-xs tabular-nums text-fg-faint">{duration(instance.readyAt ?? instance.createdAt, undefined, clock.now)}</span>{/if}
                </td>
                <td class="num text-fg-muted">{occupied ? count(route?.requests ?? 0n) : '–'}{#if route?.inFlight}<span class="text-fg-faint"> · {route.inFlight} live</span>{/if}</td>
                <td class="num text-fg-muted">{instance ? instanceMemory(instance) || '–' : '–'}</td>
                <td class="actions" onclick={(e) => e.stopPropagation()}>
                  <span>
                    {#if answering}<IconButton size="sm" icon={MessageSquare} label="Chat" href="/chat?model={encodeURIComponent(s.name)}" />{/if}
                    {#if failedSlot && s.request}
                      <Button size="sm" variant="primary" icon={RotateCcw} onclick={() => relaunchSlot(s)}>Relaunch</Button>
                    {:else if occupied}
                      <Button size="sm" variant="subtle" icon={ArrowLeftRight} onclick={() => swapSlot(s)}>Swap</Button>
                    {:else}
                      <Button size="sm" variant="primary" icon={Play} onclick={() => swapSlot(s)}>Run</Button>
                    {/if}
                    <Menu
                      size="sm"
                      items={[
                        { label: 'Settings', icon: Settings2, href: `/slots/${s.id}?tab=settings` },
                        { label: 'Evict', icon: LogOut, onSelect: () => evictSlot(s), disabled: !occupied && !s.request, detail: 'Stop the model and forget it' },
                        { label: '', separator: true },
                        { label: 'Delete', icon: Trash2, tone: 'bad', onSelect: () => deleteSlot(s) }
                      ]}
                    />
                  </span>
                </td>
              </tr>
            {/each}
            {#each standalone as i (i.id)}
              {@const route = live.routes.get(i.name)}
              <tr class="row-link" onclick={() => goto(`/instances/${i.id}`)}>
                <td class="text-fg-faint">–</td>
                <td class="font-mono text-xs text-fg">{i.name}</td>
                <td><div class="truncate text-fg" title={i.repo}>{tail(i.repo)} <span class="font-mono text-xs text-fg-muted">{groupLabel(i)}</span></div></td>
                <td class="text-fg-muted">{runtimeName(i.runtimeId)}</td>
                <td><State values={InstanceState} value={i.state} /><span class="ml-2 text-xs tabular-nums text-fg-faint">{duration(i.readyAt ?? i.createdAt, undefined, clock.now)}</span></td>
                <td class="num text-fg-muted">{count(route?.requests ?? 0n)}{#if route?.inFlight}<span class="text-fg-faint"> · {route.inFlight} live</span>{/if}</td>
                <td class="num text-fg-muted">{instanceMemory(i) || '–'}</td>
                <td class="actions" onclick={(e) => e.stopPropagation()}>
                  <span>
                    {#if route?.state === RouteState.READY}<IconButton size="sm" icon={MessageSquare} label="Chat" href="/chat?model={encodeURIComponent(i.name)}" />{/if}
                    <IconButton size="sm" icon={Square} label="Stop" class="text-bad hover:text-bad" onclick={() => stop(i)} />
                  </span>
                </td>
              </tr>
            {/each}
          {/if}
        </tbody>
      </table>
    </div>
  </Section>

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
