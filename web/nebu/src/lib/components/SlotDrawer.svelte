<script lang="ts">
  import { untrack } from 'svelte';
  import { api } from '$lib/api';
  import { live, cached, instanceLive, clock, taskFor, deviceName, groupLabel } from '$lib/state.svelte';
  import { swapSlot, evictSlot, deleteSlot } from '$lib/slotActions.svelte';
  import { slotOccupied } from '$lib/launch';
  import { policyText } from '$lib/gateway';
  import { bytes, when, duration, count, newestFirst, parseBytes } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { SlotState } from '$proto/slot_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { RouteState } from '$proto/gateway_pb';
  import { DeviceKind } from '$proto/host_pb';
  import { ArrowLeftRight, LogOut, Pencil, Trash2, ChevronRight, MessageSquare, Play } from '@lucide/svelte';
  import Drawer from './ui/Drawer.svelte';
  import Tabs from './ui/Tabs.svelte';
  import Kv from './ui/Kv.svelte';
  import Button from './ui/Button.svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import State from './ui/State.svelte';
  import Section from './ui/Section.svelte';
  import Disclosure from './ui/Disclosure.svelte';
  import ParamList from './ui/ParamList.svelte';
  import ParamForm from './ParamForm.svelte';
  import InstanceLog from './InstanceLog.svelte';
  import TaskChip from './TaskChip.svelte';

  // One slot: what it serves, its reservation and limits, its log and history, and its settings; 'new' as the id creates one
  let { id = $bindable(''), tab = $bindable('overview'), onInstance }: { id?: string; tab?: string; onInstance?: (instanceId: string) => void } = $props();

  const creating = $derived(id === 'new');
  const slot = $derived(id && !creating ? live.slots.get(id) : undefined);
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

  // The settings form
  let name = $state('');
  let description = $state('');
  let picked = $state<string[]>([]);
  let memory = $state('');
  let runtimeId = $state('');
  let values = $state<Record<string, string>>({});
  let invalid = $state(0);
  let maxInFlight = $state('');
  let rps = $state('');
  let burst = $state('');
  let timeout = $state('');
  let upstream = $state('');
  let showParams = $state(false);
  let showLimits = $state(false);
  let saving = $state(false);

  // Seconds typed by people, milliseconds on the wire
  const seconds = (ms: number | undefined) => (ms ? String(ms / 1000) : '');
  const millis = (s: string) => Math.round((parseFloat(s) || 0) * 1000);
  const whole = (s: string) => Math.max(0, Math.floor(parseFloat(s) || 0));

  const gpus = $derived((live.host?.devices ?? []).filter((d) => d.kind !== DeviceKind.CPU));
  const budget = $derived(parseBytes(memory));
  const badBudget = $derived(memory.trim() !== '' && budget === 0n);
  const nameTaken = $derived(creating && [...live.slots.values()].some((s) => s.name === name.trim()));
  const manifest = $derived(cached.runtimes.find((r) => r.manifest?.id === runtimeId)?.manifest);
  // An empty limit inherits the gateway default, so each placeholder shows that default
  const defaults = $derived(cached.gateway?.policy);
  const inherit = (n: number | undefined) => (n ? String(n) : 'none');
  const inheritSeconds = (ms: number | undefined) => (ms ? String(ms / 1000) : 'none');
  const inheritBurst = $derived(defaults?.burst ? String(defaults.burst) : defaults?.requestsPerSecond ? String(Math.ceil(defaults.requestsPerSecond)) : 'rate');
  const limitsSet = $derived([maxInFlight, rps, burst, timeout, upstream].filter((v) => v.trim()).length);
  const formOk = $derived(!!name.trim() && !nameTaken && !badBudget && invalid === 0);

  // Every opening starts the form from the slot as it stands, a new one on its form
  $effect(() => {
    const current = id;
    untrack(() => {
      if (!current) return;
      if (creating) tab = 'settings';
      fill();
    });
  });

  function fill() {
    name = slot?.name ?? '';
    description = slot?.description ?? '';
    picked = [...(slot?.deviceIds ?? [])];
    memory = slot?.memoryBytes ? bytes(slot.memoryBytes, 0).replace(' ', '') : '';
    runtimeId = slot?.runtimeId ?? '';
    values = { ...(slot?.params ?? {}) };
    maxInFlight = slot?.policy?.maxInFlight ? String(slot.policy.maxInFlight) : '';
    rps = slot?.policy?.requestsPerSecond ? String(slot.policy.requestsPerSecond) : '';
    burst = slot?.policy?.burst ? String(slot.policy.burst) : '';
    timeout = seconds(slot?.policy?.requestTimeoutMs);
    upstream = seconds(slot?.policy?.upstreamTimeoutMs);
    showParams = Object.keys(values).length > 0;
    showLimits = [maxInFlight, rps, burst, timeout, upstream].some((v) => v.trim());
  }

  function toggle(device: string) {
    picked = picked.includes(device) ? picked.filter((d) => d !== device) : [...picked, device];
  }

  async function save() {
    saving = true;
    const policy = { maxInFlight: whole(maxInFlight), requestsPerSecond: parseFloat(rps) || 0, burst: whole(burst), requestTimeoutMs: millis(timeout), upstreamTimeoutMs: millis(upstream) };
    const body = { description, deviceIds: picked, memoryBytes: budget, runtimeId, params: values, policy };
    try {
      if (slot) {
        await api.slots.updateSlot({ id: slot.id, ...body });
        ok(`Updated ${slot.name}`);
        tab = 'overview';
      } else {
        const r = await api.slots.createSlot({ name: name.trim(), ...body });
        ok(`Created ${name.trim()}`);
        if (r.slot) {
          live.slots.set(r.slot.id, r.slot);
          tab = 'overview';
          id = r.slot.id;
        }
      }
    } catch (err) {
      fail(err, slot ? 'Update failed' : 'Create failed');
    } finally {
      saving = false;
    }
  }
</script>

{#snippet secondsInput(fid: string, value: string, placeholder: string, set: (v: string) => void)}
  <div class="relative">
    <input id={fid} class="input pr-7 font-mono" inputmode="decimal" {value} {placeholder} oninput={(e) => set((e.currentTarget as HTMLInputElement).value)} />
    <span class="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-xs text-fg-faint">s</span>
  </div>
{/snippet}

<Drawer bind:id mono={!creating} title={creating ? 'New slot' : (slot?.name ?? 'Slot')} subtitle={creating ? 'A public model name with devices and memory reserved for it' : slot?.description || undefined}>
  {#snippet header()}
    {#if slot}
      <div class="flex flex-wrap items-center gap-3 text-sm text-fg-muted">
        <State values={SlotState} value={slot.state} />
        {#if route}<span class="tabular-nums">{count(route.requests)} requests{route.inFlight ? ` · ${route.inFlight} live` : ''}</span>{/if}
      </div>
      <Tabs size="sm" class="mt-3" bind:value={tab} tabs={[{ id: 'overview', label: 'Overview' }, { id: 'settings', label: 'Settings' }, { id: 'log', label: 'Log' }, { id: 'history', label: 'History', count: history.length || undefined }]} />
    {/if}
  {/snippet}

  <div class="px-6 py-5">
    {#if tab === 'settings' && (slot || creating)}
      <form
        class="grid grid-cols-1 gap-x-4 gap-y-5 sm:grid-cols-2"
        onsubmit={(e) => {
          e.preventDefault();
          if (formOk && !saving) save();
        }}
      >
        <Field label="Name" for="slot-name" hint="What clients send as the model" error={nameTaken ? 'That name is taken' : undefined}>
          <input id="slot-name" class="input font-mono" bind:value={name} placeholder="main" disabled={!creating} aria-invalid={nameTaken} autocomplete="off" spellcheck="false" />
        </Field>
        <Field label="Description" for="slot-desc">
          <input id="slot-desc" class="input" bind:value={description} placeholder="Optional" />
        </Field>

        <Field label="Devices" class="sm:col-span-2" hint={slot && slotOccupied(slot.id) ? 'Devices and memory apply on the next run' : undefined}>
          <div class="divide-y divide-line rounded-md border border-line">
            {#if gpus.length}
              <label class="flex h-9 cursor-pointer items-center gap-3 px-3 text-sm">
                <input type="checkbox" class="checkbox" checked={picked.length === 0} disabled={picked.length === 0} onchange={() => (picked = [])} />
                <span class="text-fg">Any device</span>
              </label>
            {/if}
            {#each gpus as d (d.id)}
              <label class="flex h-9 cursor-pointer items-center gap-3 px-3 text-sm">
                <input type="checkbox" class="checkbox" checked={picked.includes(d.id)} onchange={() => toggle(d.id)} />
                <span class="flex-1 truncate text-fg">{d.name || d.id}</span>
                <span class="text-xs tabular-nums text-fg-faint">{bytes(d.memoryTotalBytes, 0)}</span>
              </label>
            {:else}
              <div class="px-3 py-2.5 text-sm text-fg-faint">No accelerators probed</div>
            {/each}
          </div>
        </Field>

        <Field label="Memory cap" for="slot-memory" hint="Per device, the whole device when empty" error={badBudget ? 'A size like 8GiB' : undefined}>
          <input id="slot-memory" class="input font-mono" bind:value={memory} placeholder="8GiB" aria-invalid={badBudget} autocomplete="off" spellcheck="false" />
        </Field>
        <Field label="Runtime" for="slot-runtime">
          <Select id="slot-runtime" bind:value={runtimeId} items={[{ value: '', label: 'First compatible' }, ...cached.runtimes.map((rt) => ({ value: rt.manifest?.id ?? '', label: rt.manifest?.name ?? rt.manifest?.id ?? '', detail: rt.compatible ? undefined : 'incompatible' }))]} />
        </Field>

        <div class="sm:col-span-2">
          <Disclosure label="Parameters" summary={Object.keys(values).length ? `${Object.keys(values).length} set` : 'the runtime defaults'} bind:open={showParams}>
            <ParamForm params={manifest?.params ?? []} bind:values bind:invalid idPrefix="slot" />
          </Disclosure>
        </div>

        <div class="sm:col-span-2">
          <Disclosure label="Limits" summary={limitsSet ? `${limitsSet} set` : 'the gateway defaults'} bind:open={showLimits}>
            <div class="grid grid-cols-2 gap-4 sm:grid-cols-5">
              <Field label="In flight" for="slot-inflight">
                <input id="slot-inflight" class="input font-mono" inputmode="numeric" bind:value={maxInFlight} placeholder={inherit(defaults?.maxInFlight)} />
              </Field>
              <Field label="Per second" for="slot-rps">
                <input id="slot-rps" class="input font-mono" inputmode="decimal" bind:value={rps} placeholder={inherit(defaults?.requestsPerSecond)} />
              </Field>
              <Field label="Burst" for="slot-burst">
                <input id="slot-burst" class="input font-mono" inputmode="numeric" bind:value={burst} placeholder={inheritBurst} />
              </Field>
              <Field label="Timeout" for="slot-timeout">
                {@render secondsInput('slot-timeout', timeout, inheritSeconds(defaults?.requestTimeoutMs), (v) => (timeout = v))}
              </Field>
              <Field label="First byte" for="slot-upstream" hint="How long the runtime may take to start answering">
                {@render secondsInput('slot-upstream', upstream, inheritSeconds(defaults?.upstreamTimeoutMs), (v) => (upstream = v))}
              </Field>
            </div>
          </Disclosure>
        </div>
        <button type="submit" class="hidden" aria-hidden="true" tabindex="-1"></button>
      </form>
    {:else if slot && tab === 'overview'}
      <div class="flex flex-col gap-6">
        {#if swapTask}<TaskChip task={swapTask} label="Swapping" />{/if}
        {#if slot.error && slot.state === SlotState.FAILED}
          <div class="note note-bad">{slot.error}</div>
        {/if}

        <Section title="Serving">
          {#if instance}
            <button type="button" class="flex w-full items-center gap-3 rounded-md border border-line px-3 py-2.5 text-left transition-colors hover:border-line-strong hover:bg-raised/40" onclick={() => onInstance?.(instance.id)}>
              <div class="min-w-0 flex-1">
                <div class="truncate text-sm text-fg">{instance.repo} <span class="font-mono text-fg-muted">{groupLabel(instance)}</span></div>
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
                ['model', `${slot.request.repo} · ${groupLabel(slot.request)}`],
                ['source', slot.request.sourceId],
                ['runtime', slot.request.runtimeId || 'slot default'],
                ['params', Object.entries(slot.request.params).map(([k, v]) => `${k}=${v}`).join(' ')]
              ]}
            />
          </Section>
        {/if}
      </div>
    {:else if slot && tab === 'log'}
      {#if instance}
        {#key instance.id}<InstanceLog id={instance.id} follow={alive} height="h-[calc(100vh-17rem)]" />{/key}
      {:else}
        <p class="text-sm text-fg-faint">Empty</p>
      {/if}
    {:else if slot && tab === 'history'}
      {#if history.length === 0}
        <p class="text-sm text-fg-faint">No history</p>
      {:else}
        <table class="tbl">
          <thead><tr><th>Model</th><th>State</th><th>Runtime</th><th>Started</th><th>Ended</th></tr></thead>
          <tbody>
            {#each history as i (i.id)}
              <tr class="row-link" onclick={() => onInstance?.(i.id)}>
                <td class="font-mono text-xs">{i.repo} <span class="text-fg-muted">{groupLabel(i)}</span></td>
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

  {#snippet footer()}
    {#if creating}
      <Button variant="ghost" size="sm" onclick={() => (id = '')}>Cancel</Button>
      <span class="ml-auto"><Button variant="primary" size="sm" loading={saving} disabled={!formOk} onclick={save}>Create</Button></span>
    {:else if slot && tab === 'settings'}
      <Button variant="ghost" size="sm" onclick={() => (tab = 'overview')}>Cancel</Button>
      <span class="ml-auto"><Button variant="primary" size="sm" loading={saving} disabled={!formOk} onclick={save}>Save</Button></span>
    {:else if slot}
      <Button variant="ghost" size="sm" icon={Pencil} onclick={() => (tab = 'settings')}>Edit</Button>
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
