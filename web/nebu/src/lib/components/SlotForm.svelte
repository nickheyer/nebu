<script lang="ts">
  import { api } from '$lib/api';
  import { live, cached, orderedSlots } from '$lib/state.svelte';
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
      devices: [...(slot?.deviceIds ?? [])],
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
  let devices = $state<string[]>(start.devices);
  let memory = $state(start.memory);
  let runtimeId = $state(start.runtimeId);
  let params = $state<Record<string, string>>(start.params);
  let invalid = $state(0);
  let policy = $state(start.policy);
  let saving = $state(false);

  const creating = $derived(!slot);
  const budget = $derived(fromGib(memory));
  const badBudget = $derived(memory.trim() !== '' && budget === 0n);
  const badName = $derived(/[\s/]/.test(name));
  const nameTaken = $derived(name.trim() !== slot?.name && [...live.slots.values()].some((s) => s.name === name.trim()));
  const manifest = $derived(cached.runtimes.find((r) => r.manifest?.id === runtimeId)?.manifest);
  const occupied = $derived(!!slot && slotOccupied(slot.id));
  const count = $derived(orderedSlots().length);
  const formOk = $derived(!!name.trim() && !badName && !nameTaken && !badBudget && invalid === 0);
  // Params set for one runtime mean nothing to another
  $effect(() => {
    if (runtimeId !== start.runtimeId) params = {};
  });

  async function save() {
    saving = true;
    const body = { description, deviceIds: devices, memoryBytes: budget, runtimeId, params, policy: policyFrom(policy), position: Math.max(0, parseInt(position, 10) || 0) };
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
        <NumberInput id="slot-position" integer min={1} max={count + (creating ? 1 : 0)} bind:value={position} fallback={creating ? String(count + 1) : ''} />
      </Field>
      <Field label="Description" for="slot-desc" class="sm:col-span-2">
        <TextInput id="slot-desc" bind:value={description} empty="What this slot is for" />
      </Field>
    </div>
  </Card>

  <Card title="Reservation" meta={occupied ? 'Applies to the next run' : undefined}>
    <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
      <Field label="Devices" for="slot-devices" class="sm:col-span-2" description="Where the model in this slot is placed.">
        <DevicePicker id="slot-devices" bind:value={devices} />
      </Field>
      <Field label="Memory cap" for="slot-memory" description="Per device. Plans stay under this instead of using the whole device." error={badBudget ? 'A number of GiB, such as 8' : undefined}>
        <NumberInput id="slot-memory" min={0} step={0.5} unit="GiB" bind:value={memory} fallback="whole device" invalid={badBudget} />
      </Field>
      <Field label="Runtime" for="slot-runtime" description="Used for every run in this slot unless a run picks another.">
        <Select id="slot-runtime" bind:value={runtimeId} items={[{ value: '', label: 'First compatible runtime' }, ...cached.runtimes.map((rt) => ({ value: rt.manifest?.id ?? '', label: rt.manifest?.name ?? rt.manifest?.id ?? '', detail: rt.compatible ? undefined : 'not compatible with this host', disabled: !rt.compatible }))]} />
      </Field>
    </div>
  </Card>

  <Card title="Default parameters" meta={manifest ? `${manifest.name} · a run can override any of them` : undefined}>
    {#if manifest}
      <ParamForm params={manifest.params} bind:values={params} bind:invalid idPrefix="slot" />
    {:else}
      <p class="text-sm text-fg-faint">Pick a runtime to set default parameters.</p>
    {/if}
  </Card>

  <Card title="Limits" meta="Empty fields use the gateway defaults">
    <PolicyForm bind:fields={policy} defaults={cached.gateway?.policy} idPrefix="slot" />
  </Card>

  <div class="flex items-center justify-end gap-2">
    <Button variant="ghost" href={cancelHref}>Cancel</Button>
    <Button type="submit" variant="primary" loading={saving} disabled={!formOk}>{creating ? 'Create slot' : 'Save'}</Button>
  </div>
</form>
