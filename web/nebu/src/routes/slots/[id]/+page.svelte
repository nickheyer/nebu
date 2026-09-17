<script lang="ts">
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { tabState } from '$lib/tabs.svelte';
  import { live, cached, clock, instanceLive, taskFor, deviceName, groupLabel, runtimeName, slotByRef, answersOf } from '$lib/state.svelte';
  import { swapSlot, evictSlot, deleteSlot, relaunchSlot } from '$lib/actions.svelte';
  import { slotOccupied } from '$lib/launch';
  import { policyText, profileText } from '$lib/gateway';
  import { bytes, when, duration, count, newestFirst, ago } from '$lib/format';
  import { SlotState } from '$proto/slot_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { RouteState, type Trace } from '$proto/gateway_pb';
  import { ArrowLeftRight, LogOut, Trash2, MessageSquare, Play, RotateCcw } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Tabs from '$lib/components/ui/Tabs.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Stat from '$lib/components/ui/Stat.svelte';
  import ParamList from '$lib/components/ui/ParamList.svelte';
  import PlanView from '$lib/components/PlanView.svelte';
  import InstanceLog from '$lib/components/InstanceLog.svelte';
  import TaskChip from '$lib/components/TaskChip.svelte';
  import SlotForm from '$lib/components/SlotForm.svelte';
  import TraceTable from '$lib/components/TraceTable.svelte';
  import TraceDetail from '$lib/components/TraceDetail.svelte';

  const tabs = [
    { id: 'overview', label: 'Overview' },
    { id: 'requests', label: 'Requests' },
    { id: 'log', label: 'Log' },
    { id: 'history', label: 'History' },
    { id: 'settings', label: 'Settings' }
  ];

  const id = $derived(page.params.id ?? '');
  const slot = $derived(slotByRef(id));
  const tab = tabState(() => tabs.map((t) => t.id), () => 'overview');
  const loading = $derived(!live.ready && !live.error);
  const instance = $derived(slot?.instanceId ? live.instances.get(slot.instanceId) : undefined);
  const alive = $derived(instanceLive(instance));
  const occupied = $derived(!!slot && slotOccupied(slot.id));
  const route = $derived(slot ? live.routes.get(slot.name) : undefined);
  const answering = $derived(route?.state === RouteState.READY);
  const swapTask = $derived(slot ? taskFor('swap', { slot: slot.id }) : undefined);
  const install = $derived(instance?.installId ? live.installs.get(instance.installId) : undefined);
  const devices = $derived((slot?.deviceIds ?? []).map(deviceName));
  const history = $derived(
    [...live.instances.values()]
      .filter((i) => i.slotId === slot?.id && i.id !== slot?.instanceId)
      .sort(newestFirst((i) => i.createdAt))
      .slice(0, 30)
  );
  const traces = $derived(slot ? answersOf(slot.name) : []);
  let selectedTrace = $state('');

</script>

{#if loading}
  <div class="skeleton h-40" aria-busy="true"></div>
{:else if !slot}
  <PageHeader title="Slot not found" back={{ href: '/', label: 'Serve' }} />
  <Empty title="No slot named {id}">
    <Button href="/">Back to Serve</Button>
  </Empty>
{:else}
  <PageHeader title={slot.name} mono back={{ href: '/', label: 'Serve' }}>
    {#snippet meta()}
      <span class="font-mono text-fg-faint">#{slot.position}</span>
      <State values={SlotState} value={slot.state} />
      {#if slot.description}<span>{slot.description}</span>{/if}
    {/snippet}
    {#if answering}<Button icon={MessageSquare} href="/chat?model={encodeURIComponent(slot.name)}">Chat</Button>{/if}
    {#if slot.state === SlotState.FAILED && slot.request}
      <Button variant="primary" icon={RotateCcw} onclick={() => relaunchSlot(slot)}>Relaunch</Button>
      <Button icon={ArrowLeftRight} onclick={() => swapSlot(slot)}>Swap</Button>
    {:else if occupied}
      <Button variant="primary" icon={ArrowLeftRight} onclick={() => swapSlot(slot)}>Swap</Button>
    {:else}
      <Button variant="primary" icon={Play} onclick={() => swapSlot(slot)}>Run</Button>
    {/if}
    <Menu
      items={[
        { label: 'Evict', icon: LogOut, onSelect: () => evictSlot(slot), disabled: !occupied && !slot.request, detail: 'Stop the model and forget it' },
        { label: '', separator: true },
        {
          label: 'Delete slot',
          icon: Trash2,
          tone: 'bad',
          onSelect: async () => {
            if (await deleteSlot(slot)) goto('/');
          }
        }
      ]}
    />
    {#snippet below()}
      <Tabs tabs={tabs.map((t) => (t.id === 'history' ? { ...t, count: history.length || undefined } : t.id === 'requests' ? { ...t, count: traces.length || undefined } : t))} bind:value={tab.value} />
    {/snippet}
  </PageHeader>

  {#if tab.value === 'overview'}
    <div class="flex flex-col gap-5">
      {#if swapTask}<div class="card flex items-center gap-3 px-4 py-3"><TaskChip task={swapTask} label="Swapping" /></div>{/if}
      {#if slot.error && slot.state === SlotState.FAILED}<div class="note note-bad">{slot.error}</div>{/if}

      <div class="grid grid-cols-1 gap-5 xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <Card title="Serving">
          {#if instance}
            <div class="flex flex-col gap-4">
              <div class="flex flex-wrap items-center gap-x-4 gap-y-1">
                <a class="link text-sm" href="/instances/{instance.id}">{instance.repo}</a>
                <span class="font-mono text-xs text-fg-muted">{groupLabel(instance)}</span>
                <State values={InstanceState} value={instance.state} />
              </div>
              <div class="grid grid-cols-2 gap-4 sm:grid-cols-4">
                <Stat label="Runtime" value={runtimeName(instance.runtimeId)} sub={install?.version} />
                <Stat label="Uptime" value={alive ? duration(instance.readyAt ?? instance.createdAt, undefined, clock.now) : '–'} />
                <Stat label="Requests" value={count(route?.requests ?? 0n)} sub={route?.inFlight ? `${route.inFlight} in flight` : ''} />
                <Stat label="Endpoint" value={instance.endpoint || '–'} mono />
              </div>
              {#if instance.plan}
                <div>
                  <div class="caps mb-2 text-fg-faint">Memory plan</div>
                  <PlanView plan={instance.plan} compact />
                </div>
              {/if}
              {#if Object.keys(instance.params).length}
                <div>
                  <div class="caps mb-2 text-fg-faint">Parameters</div>
                  <ParamList params={instance.params} />
                </div>
              {/if}
            </div>
          {:else if slot.request}
            <div class="flex flex-col gap-3">
              <div class="text-sm text-fg">{slot.request.repo} <span class="font-mono text-xs text-fg-muted">{groupLabel(slot.request)}</span></div>
              <p class="text-sm text-fg-muted">{slot.state === SlotState.FAILED ? 'The last launch failed. Relaunch to try again, or swap in another model.' : 'Starting.'}</p>
            </div>
          {:else}
            <Empty compact title="Nothing is running in this slot">
              <Button size="sm" variant="primary" icon={Play} onclick={() => swapSlot(slot)}>Run a model</Button>
            </Empty>
          {/if}
        </Card>

        <Card title="Reservation">
          <Kv
            items={[
              ['Devices', devices.length ? devices.join(', ') : 'Any'],
              ['Memory cap', slot.memoryBytes ? `${bytes(slot.memoryBytes)} per device` : 'Whole device'],
              ['Runtime', slot.runtimeId ? runtimeName(slot.runtimeId) : 'First compatible'],
              ['Limits', policyText(slot.policy, cached.gateway?.policy)],
              ['Shaping', profileText(slot.profile, instance?.template)],
              ['Route', route ? `${route.state === RouteState.READY ? 'Ready' : route.state === RouteState.DRAINING ? 'Draining' : 'Waiting'} · ${count(route.requests)} requests` : '–'],
              ['Created', when(slot.createdAt)],
              ['Updated', when(slot.updatedAt)]
            ]}
          />
          {#if Object.keys(slot.params).length}
            <div class="mt-4">
              <div class="caps mb-2 text-fg-faint">Default parameters</div>
              <ParamList params={slot.params} />
            </div>
          {/if}
        </Card>
      </div>
    </div>
  {:else if tab.value === 'requests'}
    <div class="grid grid-cols-1 gap-6 {selectedTrace ? 'xl:grid-cols-[minmax(0,1fr)_28rem]' : ''}">
      <TraceTable {traces} selected={selectedTrace} onSelect={(t: Trace) => (selectedTrace = selectedTrace === t.id ? '' : t.id)} showRoute={false} compact={!!selectedTrace} />
      {#if selectedTrace}
        <Card title="Request" class="xl:sticky xl:top-8 xl:self-start"><TraceDetail id={selectedTrace} /></Card>
      {/if}
    </div>
  {:else if tab.value === 'log'}
    {#if instance}
      {#key instance.id}<InstanceLog id={instance.id} follow={alive} height="h-[calc(100vh-18rem)]" />{/key}
    {:else}
      <Empty compact title="Nothing is running in this slot" />
    {/if}
  {:else if tab.value === 'history'}
    {#if history.length === 0}
      <Empty compact title="No earlier instances in this slot" />
    {:else}
      <div class="overflow-x-auto">
          <table class="tbl">
            <thead><tr><th>Model</th><th>State</th><th>Runtime</th><th>Started</th><th>Ended</th></tr></thead>
            <tbody>
              {#each history as i (i.id)}
                <tr class="row-link" onclick={() => goto(`/instances/${i.id}`)}>
                  <td class="font-mono text-xs">{i.repo} <span class="text-fg-muted">{groupLabel(i)}</span></td>
                  <td><State values={InstanceState} value={i.state} /></td>
                  <td class="text-fg-muted">{runtimeName(i.runtimeId)}</td>
                  <td class="text-fg-muted" title={when(i.createdAt)}>{ago(i.createdAt, clock.now)}</td>
                  <td class="text-fg-muted">{when(i.stoppedAt)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
      </div>
    {/if}
  {:else if tab.value === 'settings'}
    {#key slot.id + slot.updatedAt?.seconds}
      <SlotForm {slot} cancelHref="/slots/{slot.id}" onSaved={() => (tab.value = 'overview')} />
    {/key}
  {/if}
{/if}
