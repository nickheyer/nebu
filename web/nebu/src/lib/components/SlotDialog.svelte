<script lang="ts">
  import { api } from '$lib/api';
  import { live, profileParams } from '$lib/state.svelte';
  import { bytes, parseBytes } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import type { Slot } from '$proto/slot_pb';
  import type { RuntimeStatus } from '$proto/runtime_pb';
  import { DeviceKind } from '$proto/host_pb';
  import { Check } from '@lucide/svelte';
  import Dialog from './ui/Dialog.svelte';
  import Field from './ui/Field.svelte';
  import Button from './ui/Button.svelte';
  import ParamForm from './ParamForm.svelte';

  let { open = $bindable(false), slot = null, onDone }: { open?: boolean; slot?: Slot | null; onDone?: (s: Slot) => void } = $props();

  let name = $state('');
  let description = $state('');
  let devices = $state<string[]>([]);
  let memory = $state('');
  let runtimeId = $state('');
  let values = $state<Record<string, string>>({});
  let invalid = $state(0);
  let runtimes = $state<RuntimeStatus[]>([]);
  let maxInFlight = $state('');
  let rps = $state('');
  let burst = $state('');
  let timeout = $state('');
  let upstream = $state('');
  let saving = $state(false);

  // Seconds typed by people, milliseconds on the wire
  const seconds = (ms: number | undefined) => (ms ? String(ms / 1000) : '');
  const millis = (s: string) => Math.round((parseFloat(s) || 0) * 1000);
  const whole = (s: string) => Math.max(0, Math.floor(parseFloat(s) || 0));

  const editing = $derived(!!slot);
  const gpus = $derived((live.host?.devices ?? []).filter((d) => d.kind !== DeviceKind.CPU));
  const budget = $derived(parseBytes(memory));
  const badBudget = $derived(memory.trim() !== '' && budget === 0n);
  const nameTaken = $derived(!editing && [...live.slots.values()].some((s) => s.name === name.trim()));
  const manifest = $derived(runtimes.find((r) => r.manifest?.id === runtimeId)?.manifest);

  $effect(() => {
    if (!open) return;
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
    api.runtimes.listRuntimes({}).then((r) => (runtimes = r.runtimes)).catch(() => (runtimes = []));
  });

  function toggle(id: string) {
    devices = devices.includes(id) ? devices.filter((d) => d !== id) : [...devices, id];
  }

  async function submit() {
    saving = true;
    const policy = { maxInFlight: whole(maxInFlight), requestsPerSecond: parseFloat(rps) || 0, burst: whole(burst), requestTimeoutMs: millis(timeout), upstreamTimeoutMs: millis(upstream) };
    const body = { description, deviceIds: devices, memoryBytes: budget, runtimeId, params: values, policy };
    try {
      let out: Slot | undefined;
      if (slot) {
        out = (await api.slots.updateSlot({ id: slot.id, ...body })).slot;
        ok(`Updated ${slot.name}`, 'Limits reach the route now, devices, budget, runtime, and params on the next run');
      } else {
        out = (await api.slots.createSlot({ name: name.trim(), ...body })).slot;
        ok(`Created ${name.trim()}`, 'Drag a stored model onto it to serve');
      }
      open = false;
      if (out) onDone?.(out);
    } catch (err) {
      fail(err, slot ? 'Update failed' : 'Create failed');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog bind:open title={editing ? `Edit ${slot?.name}` : 'New slot'} description={editing ? 'Limits reach the route at once, the reservation applies the next time something runs in the slot' : 'A slot reserves devices and memory under one public name that never goes away'} size="lg">
  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
    <Field label="Name" for="slot-name" hint="Becomes the model name clients send to the gateway">
      <input id="slot-name" class="input font-mono" bind:value={name} placeholder="main" disabled={editing} aria-invalid={nameTaken} />
      {#if nameTaken}<span class="text-xs text-bad">A slot with this name exists</span>{/if}
    </Field>
    <Field label="Description" for="slot-desc">
      <input id="slot-desc" class="input" bind:value={description} placeholder="Optional" />
    </Field>

    <Field label="Devices" hint={devices.length ? '' : 'All devices when none are picked'} class="sm:col-span-2">
      <div class="flex flex-wrap gap-2">
        {#each gpus as d (d.id)}
          {@const on = devices.includes(d.id)}
          <button
            type="button"
            class="flex items-center gap-2 rounded-lg border px-3 py-2 text-left text-sm transition-colors {on ? 'border-accent bg-accent/10 text-fg' : 'border-line bg-sunken text-fg-muted hover:border-line-strong'}"
            onclick={() => toggle(d.id)}
            aria-pressed={on}
          >
            <span class="flex h-4 w-4 items-center justify-center rounded border {on ? 'border-accent bg-accent text-accent-fg' : 'border-line-strong'}">{#if on}<Check size={11} strokeWidth={3} />{/if}</span>
            <span class="flex flex-col">
              <span class="font-medium">{d.name || d.id}</span>
              <span class="text-[11px] text-fg-faint">{bytes(d.memoryTotalBytes, 0)} · <span class="font-mono">{d.id}</span></span>
            </span>
          </button>
        {:else}
          <span class="text-sm text-fg-faint">No accelerators were probed. The slot will plan on host memory.</span>
        {/each}
      </div>
    </Field>

    <Field label="Memory budget" for="slot-memory" hint="Caps device memory the plan may use, for example 8GiB. Empty means the whole device">
      <input id="slot-memory" class="input font-mono" bind:value={memory} placeholder="8GiB" aria-invalid={badBudget} />
      {#if badBudget}<span class="text-xs text-bad">Use a size like 8GiB or 512MiB</span>{/if}
    </Field>
    <Field label="Default runtime" for="slot-runtime" hint="Used when a run does not name one">
      <select id="slot-runtime" class="input" bind:value={runtimeId}>
        <option value="">First compatible runtime</option>
        {#each runtimes as rt (rt.manifest?.id)}
          <option value={rt.manifest?.id}>{rt.manifest?.name ?? rt.manifest?.id}{rt.compatible ? '' : ' · incompatible'}</option>
        {/each}
      </select>
    </Field>

    <div class="sm:col-span-2">
      <div class="mb-2 flex items-baseline gap-2">
        <span class="text-xs font-medium text-fg-muted">Default parameters</span>
        <span class="text-[11.5px] text-fg-faint">{manifest ? "Over the runtime's default profile, under the run's own params" : 'Pick a runtime for a typed form, or name params directly'}</span>
      </div>
      <ParamForm params={manifest?.params ?? []} bind:values bind:invalid inherited={profileParams(runtimeId)} idPrefix="slot" />
    </div>

    <div class="sm:col-span-2">
      <div class="mb-2 flex items-baseline gap-2">
        <span class="text-xs font-medium text-fg-muted">Request limits</span>
        <span class="text-[11.5px] text-fg-faint">What the slot's route enforces at the gateway. Empty inherits the gateway default</span>
      </div>
      <div class="grid grid-cols-2 gap-4 sm:grid-cols-5">
        <Field label="In flight" for="slot-inflight" hint="Requests at once">
          <input id="slot-inflight" class="input font-mono" inputmode="numeric" bind:value={maxInFlight} placeholder="inherit" />
        </Field>
        <Field label="Per second" for="slot-rps" hint="Sustained rate">
          <input id="slot-rps" class="input font-mono" inputmode="decimal" bind:value={rps} placeholder="inherit" />
        </Field>
        <Field label="Burst" for="slot-burst" hint="Absorbed at once">
          <input id="slot-burst" class="input font-mono" inputmode="numeric" bind:value={burst} placeholder="rate" />
        </Field>
        <Field label="Timeout" for="slot-timeout" hint="Whole request, seconds">
          <input id="slot-timeout" class="input font-mono" inputmode="decimal" bind:value={timeout} placeholder="inherit" />
        </Field>
        <Field label="First byte" for="slot-upstream" hint="Runtime must answer within, seconds">
          <input id="slot-upstream" class="input font-mono" inputmode="decimal" bind:value={upstream} placeholder="inherit" />
        </Field>
      </div>
    </div>
  </div>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button variant="primary" loading={saving} onclick={submit} disabled={!name.trim() || nameTaken || badBudget || invalid > 0}>{editing ? 'Save' : 'Create slot'}</Button>
  {/snippet}
</Dialog>
