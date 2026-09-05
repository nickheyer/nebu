<script lang="ts">
  import { api } from '$lib/api';
  import { live, cached, profileParams } from '$lib/state.svelte';
  import { slotOccupied } from '$lib/launch';
  import { createForm } from '$lib/form.svelte';
  import { bytes, parseBytes } from '$lib/format';
  import type { Slot } from '$proto/slot_pb';
  import { DeviceKind } from '$proto/host_pb';
  import FormDialog from './ui/FormDialog.svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Disclosure from './ui/Disclosure.svelte';
  import ParamForm from './ParamForm.svelte';

  let { open = $bindable(false), slot = null }: { open?: boolean; slot?: Slot | null } = $props();

  let name = $state('');
  let description = $state('');
  let devices = $state<string[]>([]);
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

  // Seconds typed by people, milliseconds on the wire
  const seconds = (ms: number | undefined) => (ms ? String(ms / 1000) : '');
  const millis = (s: string) => Math.round((parseFloat(s) || 0) * 1000);
  const whole = (s: string) => Math.max(0, Math.floor(parseFloat(s) || 0));

  const editing = $derived(!!slot);
  const gpus = $derived((live.host?.devices ?? []).filter((d) => d.kind !== DeviceKind.CPU));
  const budget = $derived(parseBytes(memory));
  const badBudget = $derived(memory.trim() !== '' && budget === 0n);
  const nameTaken = $derived(!editing && [...live.slots.values()].some((s) => s.name === name.trim()));
  const manifest = $derived(cached.runtimes.find((r) => r.manifest?.id === runtimeId)?.manifest);
  // An empty limit inherits the gateway default, so each placeholder shows that default
  const defaults = $derived(cached.gateway?.policy);
  const inherit = (n: number | undefined) => (n ? String(n) : 'none');
  const inheritSeconds = (ms: number | undefined) => (ms ? String(ms / 1000) : 'none');
  const inheritBurst = $derived(defaults?.burst ? String(defaults.burst) : defaults?.requestsPerSecond ? String(Math.ceil(defaults.requestsPerSecond)) : 'rate');
  const limitsSet = $derived([maxInFlight, rps, burst, timeout, upstream].filter((v) => v.trim()).length);

  function toggle(id: string) {
    devices = devices.includes(id) ? devices.filter((d) => d !== id) : [...devices, id];
  }

  const form = createForm({
    open: () => open,
    close: () => (open = false),
    reset() {
      name = slot?.name ?? '';
      description = slot?.description ?? '';
      devices = [...(slot?.deviceIds ?? [])];
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
    },
    async submit() {
      const policy = { maxInFlight: whole(maxInFlight), requestsPerSecond: parseFloat(rps) || 0, burst: whole(burst), requestTimeoutMs: millis(timeout), upstreamTimeoutMs: millis(upstream) };
      const body = { description, deviceIds: devices, memoryBytes: budget, runtimeId, params: values, policy };
      if (slot) {
        await api.slots.updateSlot({ id: slot.id, ...body });
        return { title: `Updated ${slot.name}` };
      }
      await api.slots.createSlot({ name: name.trim(), ...body });
      return { title: `Created ${name.trim()}` };
    },
    failTitle: () => (slot ? 'Update failed' : 'Create failed')
  });
</script>

{#snippet secondsInput(id: string, value: string, placeholder: string, set: (v: string) => void)}
  <div class="relative">
    <input {id} class="input pr-7 font-mono" inputmode="decimal" {value} {placeholder} oninput={(e) => set((e.currentTarget as HTMLInputElement).value)} />
    <span class="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-xs text-fg-faint">s</span>
  </div>
{/snippet}

<FormDialog
  bind:open
  title={editing ? `Edit ${slot?.name}` : 'New slot'}
  size="lg"
  action={editing ? 'Save' : 'Create'}
  saving={form.saving}
  disabled={!name.trim() || nameTaken || badBudget || invalid > 0}
  note={editing && slotOccupied(slot?.id) ? 'Devices and memory apply on the next run' : ''}
  onsubmit={form.run}
>
  <div class="grid grid-cols-1 gap-x-4 gap-y-5 sm:grid-cols-2">
    <Field label="Name" for="slot-name" info="What clients send as the model" error={nameTaken ? 'That name is taken' : undefined}>
      <input id="slot-name" class="input font-mono" bind:value={name} placeholder="main" disabled={editing} aria-invalid={nameTaken} autocomplete="off" spellcheck="false" />
    </Field>
    <Field label="Description" for="slot-desc">
      <input id="slot-desc" class="input" bind:value={description} placeholder="Optional" />
    </Field>

    <Field label="Devices" class="sm:col-span-2">
      <div class="divide-y divide-line rounded-md border border-line">
        {#if gpus.length}
          <label class="flex h-9 cursor-pointer items-center gap-3 px-3 text-sm">
            <input type="checkbox" class="checkbox" checked={devices.length === 0} disabled={devices.length === 0} onchange={() => (devices = [])} />
            <span class="text-fg">Any device</span>
          </label>
        {/if}
        {#each gpus as d (d.id)}
          <label class="flex h-9 cursor-pointer items-center gap-3 px-3 text-sm">
            <input type="checkbox" class="checkbox" checked={devices.includes(d.id)} onchange={() => toggle(d.id)} />
            <span class="flex-1 truncate text-fg">{d.name || d.id}</span>
            <span class="text-xs tabular-nums text-fg-faint">{bytes(d.memoryTotalBytes, 0)}</span>
          </label>
        {:else}
          <div class="px-3 py-2.5 text-sm text-fg-faint">No accelerators found</div>
        {/each}
      </div>
    </Field>

    <Field label="Memory cap" for="slot-memory" info="Per device. Empty means the whole device." error={badBudget ? 'A size like 8GiB' : undefined}>
      <input id="slot-memory" class="input font-mono" bind:value={memory} placeholder="8GiB" aria-invalid={badBudget} autocomplete="off" spellcheck="false" />
    </Field>
    <Field label="Runtime" for="slot-runtime">
      <Select id="slot-runtime" bind:value={runtimeId} items={[{ value: '', label: 'First compatible' }, ...cached.runtimes.map((rt) => ({ value: rt.manifest?.id ?? '', label: rt.manifest?.name ?? rt.manifest?.id ?? '', detail: rt.compatible ? undefined : 'incompatible' }))]} />
    </Field>

    <div class="sm:col-span-2">
      <Disclosure label="Parameters" summary={Object.keys(values).length ? `${Object.keys(values).length} set` : 'runtime defaults'} bind:open={showParams}>
        <ParamForm params={manifest?.params ?? []} bind:values bind:invalid inherited={profileParams(runtimeId)} idPrefix="slot" />
      </Disclosure>
    </div>

    <div class="sm:col-span-2">
      <Disclosure label="Limits" summary={limitsSet ? `${limitsSet} set` : 'gateway defaults'} bind:open={showLimits}>
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
          <Field label="First byte" for="slot-upstream" info="How long the runtime may take to start answering">
            {@render secondsInput('slot-upstream', upstream, inheritSeconds(defaults?.upstreamTimeoutMs), (v) => (upstream = v))}
          </Field>
        </div>
      </Disclosure>
    </div>
  </div>
</FormDialog>
