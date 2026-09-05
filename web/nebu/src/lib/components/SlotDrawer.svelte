<script lang="ts">
  import { live, cached, instanceLive, clock, taskFor, deviceName } from '$lib/state.svelte';
  import { swapSlot, editSlot, evictSlot, deleteSlot } from '$lib/slotActions.svelte';
  import { policyText } from '$lib/gateway';
  import { bytes, when, duration, count, newestFirst } from '$lib/format';
  import { SlotState } from '$proto/slot_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { RouteState } from '$proto/gateway_pb';
  import { ArrowLeftRight, LogOut, Pencil, Trash2, ChevronRight, MessageSquare, Play } from '@lucide/svelte';
  import Drawer from './ui/Drawer.svelte';
  import Tabs from './ui/Tabs.svelte';
  import Kv from './ui/Kv.svelte';
  import Button from './ui/Button.svelte';
  import State from './ui/State.svelte';
  import Section from './ui/Section.svelte';
  import ParamList from './ui/ParamList.svelte';
  import InstanceLog from './InstanceLog.svelte';
  import TaskChip from './TaskChip.svelte';

  let { id = $bindable(''), onInstance }: { id?: string; onInstance?: (instanceId: string) => void } = $props();

  let tab = $state('overview');
  const slot = $derived(id ? live.slots.get(id) : undefined);
  const instance = $derived(slot?.instanceId ? live.instances.get(slot.instanceId) : undefined);
  const alive = $derived(instanceLive(instance));
  const route = $derived(slot ? live.routes.get(slot.name) : undefined);
  const answering = $derived(route?.state === RouteState.READY);
  const swapTask = $derived(slot ? taskFor('swap', { slot: slot.id }) : undefined);
  const devices = $derived((slot?.deviceIds ?? []).map(deviceName));
  const history = $derived(
    [...live.instances.values()]
      .filter((i) => i.slotId === id && i.id !== slot?.instanceId)
      .sort(newestFirst((i) => i.createdAt))
      .slice(0, 12)
  );

  $effect(() => {
    if (id) tab = 'overview';
  });
</script>

<Drawer bind:id mono title={slot?.name ?? 'Slot'} subtitle={slot?.description || undefined}>
  {#snippet header()}
    {#if slot}
      <div class="flex flex-wrap items-center gap-3 text-sm text-fg-muted">
        <State values={SlotState} value={slot.state} />
        {#if route}<span class="tabular-nums">{count(route.requests)} requests{route.inFlight ? ` · ${route.inFlight} live` : ''}</span>{/if}
      </div>
      <Tabs size="sm" class="mt-3" bind:value={tab} tabs={[{ id: 'overview', label: 'Overview' }, { id: 'log', label: 'Log' }, { id: 'history', label: 'History', count: history.length || undefined }]} />
    {/if}
  {/snippet}

  {#if slot}
    <div class="px-6 py-5">
      {#if tab === 'overview'}
        <div class="flex flex-col gap-6">
          {#if swapTask}<TaskChip task={swapTask} label="Swapping" />{/if}
          {#if slot.error && slot.state === SlotState.FAILED}
            <div class="note note-bad">{slot.error}</div>
          {/if}

          <Section title="Serving">
            {#if instance}
              <button type="button" class="flex w-full items-center gap-3 rounded-md border border-line px-3 py-2.5 text-left transition-colors hover:border-line-strong hover:bg-raised/40" onclick={() => onInstance?.(instance.id)}>
                <div class="min-w-0 flex-1">
                  <div class="truncate text-sm text-fg">{instance.repo} <span class="font-mono text-fg-muted">{instance.group}</span></div>
                  <div class="mt-0.5 flex flex-wrap gap-x-3 text-xs text-fg-faint">
                    <span>{instance.runtimeId}</span>
                    {#if instance.pid}<span>pid {instance.pid}</span>{/if}
                    {#if instance.endpoint}<span class="font-mono">{instance.endpoint}</span>{/if}
                    {#if alive}<span>up {duration(instance.readyAt ?? instance.createdAt, undefined, clock.now)}</span>{/if}
                  </div>
                </div>
                <State values={InstanceState} value={instance.state} />
                <ChevronRight size={15} class="text-fg-faint" />
              </button>
            {:else}
              <div class="flex items-center gap-3 rounded-md border border-dashed border-line px-3 py-2.5">
                <span class="flex-1 text-sm text-fg-muted">Empty</span>
                <Button size="sm" variant="primary" icon={Play} onclick={() => swapSlot(slot)}>Run</Button>
              </div>
            {/if}
          </Section>

          <Section title="Reservation">
            <Kv
              columns={2}
              items={[
                ['devices', devices.length ? devices.join(', ') : 'any'],
                ['memory cap', slot.memoryBytes ? bytes(slot.memoryBytes) : 'whole device'],
                ['runtime', slot.runtimeId || 'first compatible'],
                ['limits', policyText(slot.policy, cached.gateway?.policy)],
                ['created', when(slot.createdAt)],
                ['updated', when(slot.updatedAt)]
              ]}
            />
            {#if Object.keys(slot.params).length}<ParamList params={slot.params} class="mt-3" />{/if}
          </Section>

          {#if slot.request}
            <Section title="Last request">
              <Kv
                mono
                items={[
                  ['model', `${slot.request.repo} · ${slot.request.group}`],
                  ['source', slot.request.sourceId],
                  ['runtime', slot.request.runtimeId || 'slot default'],
                  ['params', Object.entries(slot.request.params).map(([k, v]) => `${k}=${v}`).join(' ')]
                ]}
              />
            </Section>
          {/if}
        </div>
      {:else if tab === 'log'}
        {#if instance}
          {#key instance.id}<InstanceLog id={instance.id} follow={alive} height="h-[calc(100vh-17rem)]" />{/key}
        {:else}
          <p class="text-sm text-fg-faint">Empty</p>
        {/if}
      {:else if tab === 'history'}
        {#if history.length === 0}
          <p class="text-sm text-fg-faint">No history</p>
        {:else}
          <table class="tbl">
            <thead><tr><th>Model</th><th>State</th><th>Runtime</th><th>Started</th><th>Ended</th></tr></thead>
            <tbody>
              {#each history as i (i.id)}
                <tr class="row-link" onclick={() => onInstance?.(i.id)}>
                  <td class="font-mono text-xs">{i.repo} <span class="text-fg-muted">{i.group}</span></td>
                  <td><State values={InstanceState} value={i.state} /></td>
                  <td class="text-fg-muted">{i.runtimeId}</td>
                  <td class="text-fg-muted">{when(i.createdAt)}</td>
                  <td class="text-fg-muted">{when(i.stoppedAt)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {/if}
    </div>
  {/if}

  {#snippet footer()}
    {#if slot}
      <Button variant="ghost" size="sm" icon={Pencil} onclick={() => editSlot(slot)}>Edit</Button>
      <Button variant="ghost" size="sm" icon={LogOut} disabled={!alive} onclick={() => evictSlot(slot)}>Evict</Button>
      <Button
        variant="ghost"
        size="sm"
        icon={Trash2}
        class="text-bad hover:text-bad"
        onclick={async () => {
          if (await deleteSlot(slot)) id = '';
        }}>Delete</Button
      >
      <span class="ml-auto flex items-center gap-2">
        {#if answering}<Button size="sm" icon={MessageSquare} href="/chat?model={encodeURIComponent(slot.name)}">Chat</Button>{/if}
        <Button variant="primary" size="sm" icon={alive ? ArrowLeftRight : Play} onclick={() => swapSlot(slot)}>{alive ? 'Swap' : 'Run'}</Button>
      </span>
    {/if}
  {/snippet}
</Drawer>
