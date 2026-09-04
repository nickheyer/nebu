<script lang="ts">
  import { api } from '$lib/api';
  import { live, instanceLive, clock, taskFor } from '$lib/state.svelte';
  import { swapSlot, editSlot, evictSlot, deleteSlot } from '$lib/slotActions.svelte';
  import { policyText } from '$lib/gateway';
  import { bytes, when, duration, enumLabel, count } from '$lib/format';
  import { SlotState } from '$proto/slot_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { RouteState, type Policy } from '$proto/gateway_pb';
  import { ArrowLeftRight, LogOut, Pencil, Trash2, ExternalLink, MessageSquare } from '@lucide/svelte';
  import Drawer from './ui/Drawer.svelte';
  import Tabs from './ui/Tabs.svelte';
  import Kv from './ui/Kv.svelte';
  import Button from './ui/Button.svelte';
  import StateBadge from './ui/StateBadge.svelte';
  import InstanceLog from './InstanceLog.svelte';
  import TaskChip from './TaskChip.svelte';

  let { id = $bindable(''), onInstance }: { id?: string; onInstance?: (instanceId: string) => void } = $props();

  let tab = $state('overview');
  let defaults = $state<Policy | undefined>();
  const slot = $derived(id ? live.slots.get(id) : undefined);
  const instance = $derived(slot?.instanceId ? live.instances.get(slot.instanceId) : undefined);
  const alive = $derived(instanceLive(instance));
  const route = $derived(slot ? live.routes.get(slot.name) : undefined);
  const swapTask = $derived(slot ? taskFor('swap', { slot: slot.id }) : undefined);
  const devices = $derived((slot?.deviceIds ?? []).map((d) => live.host?.devices.find((x) => x.id === d)?.name ?? d));
  const history = $derived([...live.instances.values()].filter((i) => i.slotId === id && i.id !== slot?.instanceId).sort((a, b) => Number((b.createdAt?.seconds ?? 0n) - (a.createdAt?.seconds ?? 0n))).slice(0, 8));

  // The gateway default sits under every zero field of the slot's limits
  $effect(() => {
    if (!id) return;
    tab = 'overview';
    api.gateway.getGatewayStatus({}).then((r) => (defaults = r.status?.policy)).catch(() => (defaults = undefined));
  });
</script>

<Drawer
  open={!!id}
  onOpenChange={(v) => {
    if (!v) id = '';
  }}
  title={slot?.name ?? 'Slot'}
  subtitle={slot?.id}
  width="lg"
>
  {#snippet header()}
    {#if slot}
      <div class="flex flex-wrap items-center gap-2">
        <StateBadge values={SlotState} value={slot.state} />
        {#if slot.description}<span class="text-xs text-fg-muted">{slot.description}</span>{/if}
        {#if route}
          <span class="text-xs tabular-nums {route.state === RouteState.READY ? 'text-ok' : 'text-fg-faint'}">{enumLabel(RouteState, route.state)} · {count(route.requests)} requests{route.inFlight ? ` · ${route.inFlight} in flight` : ''}</span>
        {/if}
      </div>
      <div class="mt-3">
        <Tabs size="sm" bind:value={tab} tabs={[{ id: 'overview', label: 'Overview' }, { id: 'log', label: 'Log' }, { id: 'history', label: 'History', count: history.length || undefined }]} />
      </div>
    {/if}
  {/snippet}

  {#if slot}
    <div class="px-5 py-4">
      {#if tab === 'overview'}
        <div class="flex flex-col gap-5">
          {#if swapTask}<TaskChip task={swapTask} label="Swap in progress" />{/if}
          {#if slot.error && slot.state === SlotState.FAILED}
            <div class="rounded-lg border border-bad/30 bg-bad/10 px-3 py-2 text-sm leading-6 text-bad">{slot.error}</div>
          {/if}

          <section>
            <h3 class="mb-2 text-[11px] font-semibold tracking-wider text-fg-faint uppercase">Serving</h3>
            {#if instance}
              <button class="flex w-full items-center gap-3 rounded-lg border border-line bg-sunken px-3 py-2.5 text-left transition-colors hover:border-line-strong" onclick={() => onInstance?.(instance.id)}>
                <div class="min-w-0 flex-1">
                  <div class="truncate font-mono text-sm text-fg">{instance.repo} <span class="text-fg-muted">· {instance.group}</span></div>
                  <div class="mt-0.5 flex flex-wrap gap-x-3 text-xs text-fg-faint">
                    <span>{instance.runtimeId}</span>
                    {#if instance.pid}<span>pid {instance.pid}</span>{/if}
                    {#if instance.endpoint}<span class="font-mono">{instance.endpoint}</span>{/if}
                    {#if alive}<span>up {duration(instance.readyAt ?? instance.createdAt, undefined, clock.now)}</span>{/if}
                  </div>
                </div>
                <StateBadge values={InstanceState} value={instance.state} size="xs" />
                <ExternalLink size={13} class="text-fg-faint" />
              </button>
            {:else}
              <div class="rounded-lg border border-dashed border-line px-3 py-4 text-center text-sm text-fg-faint">Nothing is serving this slot. The gateway answers 503 with a retry hint for <span class="font-mono text-fg-muted">{slot.name}</span>.</div>
            {/if}
          </section>

          <section>
            <h3 class="mb-2 text-[11px] font-semibold tracking-wider text-fg-faint uppercase">Reservation</h3>
            <Kv
              columns={2}
              items={[
                ['devices', devices.length ? devices.join(', ') : 'all devices'],
                ['budget', slot.memoryBytes ? bytes(slot.memoryBytes) : 'whole devices'],
                ['runtime', slot.runtimeId || 'first compatible'],
                ['public name', slot.name],
                ['limits', policyText(slot.policy, defaults)],
                ['created', when(slot.createdAt)],
                ['updated', when(slot.updatedAt)]
              ]}
            />
            {#if Object.keys(slot.params).length}
              <div class="mt-3 flex flex-wrap gap-1.5">
                {#each Object.entries(slot.params) as [k, v] (k)}
                  <span class="rounded border border-line bg-sunken px-1.5 py-0.5 font-mono text-[11px]"><span class="text-fg-faint">{k}=</span><span class="text-fg">{v}</span></span>
                {/each}
              </div>
            {/if}
          </section>

          {#if slot.request}
            <section>
              <h3 class="mb-2 text-[11px] font-semibold tracking-wider text-fg-faint uppercase">Last request</h3>
              <Kv
                mono
                items={[
                  ['model', `${slot.request.repo} · ${slot.request.group}`],
                  ['source', slot.request.sourceId],
                  ['runtime', slot.request.runtimeId || 'slot default'],
                  ['params', Object.entries(slot.request.params).map(([k, v]) => `${k}=${v}`).join(' ')]
                ]}
              />
            </section>
          {/if}
        </div>
      {:else if tab === 'log'}
        {#if instance}
          {#key instance.id}<InstanceLog id={instance.id} follow={alive} height="h-[calc(100vh-16rem)]" />{/key}
        {:else}
          <p class="text-sm text-fg-faint">Nothing is running in this slot.</p>
        {/if}
      {:else if tab === 'history'}
        {#if history.length === 0}
          <p class="text-sm text-fg-faint">No earlier instances ran in this slot.</p>
        {:else}
          <table class="tbl">
            <thead><tr><th>model</th><th>state</th><th>runtime</th><th>started</th><th>ended</th></tr></thead>
            <tbody>
              {#each history as i (i.id)}
                <tr class="row-link" onclick={() => onInstance?.(i.id)}>
                  <td class="font-mono text-xs">{i.repo} <span class="text-fg-muted">· {i.group}</span></td>
                  <td><StateBadge values={InstanceState} value={i.state} size="xs" /></td>
                  <td class="text-xs">{i.runtimeId}</td>
                  <td class="text-xs text-fg-muted">{when(i.createdAt)}</td>
                  <td class="text-xs text-fg-muted">{when(i.stoppedAt)}</td>
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
      {#if route?.state === RouteState.READY}
        <Button variant="outline" size="sm" icon={MessageSquare} class="ml-auto" href="/chat?model={encodeURIComponent(slot.name)}">Chat</Button>
      {/if}
      <Button variant="primary" size="sm" icon={ArrowLeftRight} class={route?.state === RouteState.READY ? '' : 'ml-auto'} onclick={() => swapSlot(slot)}>{alive ? 'Swap model' : 'Run a model'}</Button>
    {/if}
  {/snippet}
</Drawer>
