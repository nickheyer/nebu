<script lang="ts">
  import { api } from '$lib/api';
  import { live, cached, orderedSlots, hostGpus } from '$lib/state.svelte';
  import { slotOccupied } from '$lib/launch';
  import { policyFields, policyFrom } from '$lib/gateway';
  import { gib, fromGib } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import type { Slot } from '$proto/slot_pb';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import NumberInput from './ui/NumberInput.svelte';
  import TextInput from './ui/TextInput.svelte';
  import Button from './ui/Button.svelte';
  import Card from './ui/Card.svelte';
  import ParamForm from './ParamForm.svelte';
  import PolicyForm from './PolicyForm.svelte';
  import DevicePicker from './DevicePicker.svelte';

  // The settings of one slot, creating it when none is given
  let { slot, cancelHref = '/', onSaved }: { slot?: Slot; cancelHref?: string; onSaved: (slot: Slot) => void } = $props();

  // The form starts from the slot as it stands; the page remounts the form when the slot changes
  function initial() {
    return {
      name: slot?.name ?? '',
      description: slot?.description ?? '',
      position: slot?.position ? String(slot.position) : '',
      // A slot pinned to no device is placed on every accelerator, so it starts with every one checked
      devices: slot?.deviceIds.length ? [...slot.deviceIds] : null,
      memory: gib(slot?.memoryBytes),
      runtimeId: slot?.runtimeId ?? '',
      params: { ...(slot?.params ?? {}) } as Record<string, string>,
      policy: policyFields(slot?.policy)
    };
  }
  const start = initial();
  let name = $state(start.name);
  let description = $state(start.description);
  let position = $state(start.position);
  let devices = $state<string[] | null>(start.devices);
  let memory = $state(start.memory);
  let runtimeId = $state(start.runtimeId);
  let params = $state<Record<string, string>>(start.params);
  let invalid = $state(0);
  let policy = $state(start.policy);
  let saving = $state(false);

  const creating = $derived(!slot);
  const gpuIds = $derived(hostGpus().map((d) => d.id));
  const chosen = $derived(devices ?? gpuIds);
  const noDevices = $derived(gpuIds.length > 0 && chosen.length === 0);
  const budget = $derived(fromGib(memory));
  // The cap that applies while none is set: the whole of each chosen device, one figure when they share a size
  const deviceGib = $derived.by(() => {
    const totals = new Set(hostGpus().filter((d) => chosen.includes(d.id)).map((d) => d.memoryTotalBytes));
    return totals.size === 1 ? gib([...totals][0]) : '';
  });
  const badBudget = $derived(memory.trim() !== '' && budget === 0n);
  const badName = $derived(/[\s/]/.test(name));
  const nameTaken = $derived(name.trim() !== slot?.name && [...live.slots.values()].some((s) => s.name === name.trim()));
  const runtime = $derived(cached.runtimes.find((r) => r.runtime?.id === runtimeId)?.runtime);
  const occupied = $derived(!!slot && slotOccupied(slot.id));
  const count = $derived(orderedSlots().length);
  const formOk = $derived(!!name.trim() && !badName && !nameTaken && !badBudget && !noDevices && invalid === 0);
  // Params set for one runtime mean nothing to another
  $effect(() => {
    if (runtimeId !== start.runtimeId) params = {};
  });

  async function save() {
    saving = true;
    const body = { description, deviceIds: chosen, memoryBytes: budget, runtimeId, params, policy: policyFrom(policy), position: Math.max(0, parseInt(position, 10) || 0) };
    try {
      if (slot) {
        const r = await api.slots.updateSlot({ id: slot.id, name: name.trim(), ...body });
        ok(`Saved ${name.trim()}`);
        if (r.slot) onSaved(r.slot);
      } else {
        const r = await api.slots.createSlot({ name: name.trim(), ...body });
        ok(`Created ${name.trim()}`);
        if (r.slot) {
          live.slots.set(r.slot.id, r.slot);
          onSaved(r.slot);
        }
      }
    } catch (err) {
      fail(err, slot ? 'Save failed' : 'Create failed');
    } finally {
      saving = false;
    }
  }
</script>

<form
  class="flex flex-col gap-5"
  onsubmit={(e) => {
    e.preventDefault();
    if (formOk && !saving) save();
  }}
>
  <Card title="Name">
    <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-[minmax(0,1fr)_8rem]">
      <Field label="Name" for="slot-name" required description="Clients send this as the model name. Renaming moves the route." error={badName ? 'No spaces or slashes' : nameTaken ? 'Another slot has this name' : undefined}>
        <TextInput id="slot-name" mono bind:value={name} empty="main" invalid={badName || nameTaken} />
      </Field>
      <Field label="Position" for="slot-position" description="Order in the list.">
        <NumberInput id="slot-position" integer min={1} max={count + (creating ? 1 : 0)} bind:value={position} empty={creating ? String(count + 1) : ''} />
      </Field>
      <Field label="Description" for="slot-desc" class="sm:col-span-2">
        <TextInput id="slot-desc" bind:value={description} />
      </Field>
    </div>
  </Card>

  <Card title="Reservation" meta={occupied ? 'Applies to the next run' : undefined}>
    <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
      <Field label="Devices" for="slot-devices" class="sm:col-span-2" description="Where the model in this slot is placed." error={noDevices ? 'Pick at least one device' : undefined}>
        <DevicePicker id="slot-devices" bind:value={() => chosen, (v) => (devices = v)} />
      </Field>
      <Field label="Memory cap" for="slot-memory" description="Per device. Plans stay under this instead of using the whole device." error={badBudget ? 'A number of GiB, such as 8' : undefined}>
        <NumberInput id="slot-memory" min={0} step={0.5} unit="GiB" bind:value={memory} empty={deviceGib} invalid={badBudget} />
      </Field>
      <Field label="Runtime" for="slot-runtime" description="Used for every run in this slot unless a run picks another. Empty takes the first runtime that reads the model.">
        <Select id="slot-runtime" bind:value={runtimeId} items={[{ value: '', label: '–' }, ...cached.runtimes.map((rt) => ({ value: rt.runtime?.id ?? '', label: rt.runtime?.name ?? rt.runtime?.id ?? '', detail: rt.compatible ? undefined : 'not compatible with this host', disabled: !rt.compatible }))]} />
      </Field>
    </div>
  </Card>

  <Card title="Default parameters" meta={runtime ? `${runtime.name} · a run can override any of them` : undefined}>
    {#if runtime}
      <ParamForm params={runtime.params} bind:values={params} bind:invalid idPrefix="slot" />
    {:else}
      <p class="text-sm text-fg-faint">Pick a runtime to set default parameters.</p>
    {/if}
  </Card>

  <Card title="Limits">
    <PolicyForm bind:fields={policy} defaults={cached.gateway?.policy} idPrefix="slot" />
  </Card>

  <div class="flex items-center justify-end gap-2">
    <Button variant="ghost" href={cancelHref}>Cancel</Button>
    <Button type="submit" variant="primary" loading={saving} disabled={!formOk}>{creating ? 'Create slot' : 'Save'}</Button>
  </div>
</form>
