<script lang="ts">
  import { api } from '$lib/api';
  import { live } from '$lib/state.svelte';
  import { bytes, parseBytes, parsePairs, pairsText } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import type { Slot } from '$proto/slot_pb';
  import type { RuntimeStatus } from '$proto/runtime_pb';
  import { DeviceKind } from '$proto/host_pb';
  import { Check } from '@lucide/svelte';
  import Dialog from './ui/Dialog.svelte';
  import Field from './ui/Field.svelte';
  import Button from './ui/Button.svelte';

  let { open = $bindable(false), slot = null, onDone }: { open?: boolean; slot?: Slot | null; onDone?: (s: Slot) => void } = $props();

  let name = $state('');
  let description = $state('');
  let devices = $state<string[]>([]);
  let memory = $state('');
  let runtimeId = $state('');
  let paramsText = $state('');
  let runtimes = $state<RuntimeStatus[]>([]);
  let saving = $state(false);

  const editing = $derived(!!slot);
  const gpus = $derived((live.host?.devices ?? []).filter((d) => d.kind !== DeviceKind.CPU));
  const budget = $derived(parseBytes(memory));
  const badBudget = $derived(memory.trim() !== '' && budget === 0n);
  const nameTaken = $derived(!editing && [...live.slots.values()].some((s) => s.name === name.trim()));

  $effect(() => {
    if (!open) return;
    name = slot?.name ?? '';
    description = slot?.description ?? '';
    devices = [...(slot?.deviceIds ?? [])];
    memory = slot?.memoryBytes ? bytes(slot.memoryBytes, 0).replace(' ', '') : '';
    runtimeId = slot?.runtimeId ?? '';
    paramsText = pairsText(slot?.params);
    api.runtimes.listRuntimes({}).then((r) => (runtimes = r.runtimes)).catch(() => (runtimes = []));
  });

  function toggle(id: string) {
    devices = devices.includes(id) ? devices.filter((d) => d !== id) : [...devices, id];
  }

  async function submit() {
    saving = true;
    const body = { description, deviceIds: devices, memoryBytes: budget, runtimeId, params: parsePairs(paramsText) };
    try {
      let out: Slot | undefined;
      if (slot) {
        out = (await api.slots.updateSlot({ id: slot.id, ...body })).slot;
        ok(`Updated ${slot.name}`, 'Settings apply on the next run or swap');
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

<Dialog bind:open title={editing ? `Edit ${slot?.name}` : 'New slot'} description={editing ? 'Changes apply the next time something runs in the slot' : 'A slot reserves devices and memory under one public name that never goes away'} size="lg">
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

    <Field label="Default parameters" for="slot-params" hint="One name=value per line, merged under the run's own params" class="sm:col-span-2">
      <textarea id="slot-params" class="input h-20" bind:value={paramsText} placeholder="n_ctx=16384"></textarea>
    </Field>
  </div>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button variant="primary" loading={saving} onclick={submit} disabled={!name.trim() || nameTaken || badBudget}>{editing ? 'Save' : 'Create slot'}</Button>
  {/snippet}
</Dialog>
