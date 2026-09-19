<script lang="ts">
  import { api } from '$lib/api';
  import { live, cached, orderedSlots, hostGpus } from '$lib/state.svelte';
  import { slotOccupied } from '$lib/launch';
  import { placements, placementId, placementOf } from '$lib/instances';
  import { policyFields, policyFrom, profileValue, profileFrom } from '$lib/gateway';
  import { gib, fromGib } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { PoolKind } from '$proto/host_pb';
  import type { Slot } from '$proto/slot_pb';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Segmented from './ui/Segmented.svelte';
  import NumberInput from './ui/NumberInput.svelte';
  import TextInput from './ui/TextInput.svelte';
  import Button from './ui/Button.svelte';
  import Card from './ui/Card.svelte';
  import ParamForm from './ParamForm.svelte';
  import PolicyForm from './PolicyForm.svelte';
  import ProfileForm from './ProfileForm.svelte';
  import DevicePicker from './DevicePicker.svelte';

  let { slot, cancelHref = '/', onSaved }: { slot?: Slot; cancelHref?: string; onSaved: (slot: Slot) => void } = $props();

  // The page remounts this form when the slot changes.
  function initial() {
    return {
      name: slot?.name ?? '',
      position: slot?.position ? String(slot.position) : '',
      placement: placementId(slot?.placement),
      // An empty device list selects all GPUs.
      devices: slot?.deviceIds.length ? [...slot.deviceIds] : null,
      memory: gib(slot?.memoryBytes),
      runtimeId: slot?.runtimeId ?? '',
      params: { ...(slot?.params ?? {}) } as Record<string, string>,
      policy: policyFields(slot?.policy),
      profile: profileValue(slot?.profile)
    };
  }
  const start = initial();
  let name = $state(start.name);
  let position = $state(start.position);
  let placement = $state(start.placement);
  let devices = $state<string[] | null>(start.devices);
  let memory = $state(start.memory);
  let runtimeId = $state(start.runtimeId);
  let params = $state<Record<string, string>>(start.params);
  let invalid = $state(0);
  let policy = $state(start.policy);
  let profile = $state(start.profile);
  let saving = $state(false);

  const creating = $derived(!slot);
  const gpus = $derived(hostGpus());
  const gpuIds = $derived(gpus.map((d) => d.id));
  const ram = $derived(live.host?.pools.find((p) => p.kind === PoolKind.HOST || p.kind === PoolKind.UNIFIED));
  const hostOnly = $derived(placement === 'host');
  const placementTabs = $derived(placements.map((p) => ({ id: p.id, label: p.label, unmet: p.id !== 'host' && live.host && gpus.length === 0 ? 'No GPU probed on this host' : undefined })));
  $effect(() => {
    if (live.host && gpus.length === 0 && placement !== 'host') placement = 'host';
  });
  const pickDevices = $derived(!hostOnly && gpus.length > 1);
  const chosen = $derived(devices ?? gpuIds);
  // Selecting all GPUs also includes GPUs discovered later.
  const deviceIds = $derived(pickDevices && chosen.length < gpuIds.length ? chosen : []);
  const budget = $derived(fromGib(memory));
  // Default to full memory capacity. GPU capacities must match to show a default.
  const poolGib = $derived.by(() => {
    if (hostOnly) return gib(ram?.totalBytes);
    const totals = new Set(gpus.filter((d) => chosen.includes(d.id)).map((d) => d.memoryTotalBytes));
    return totals.size === 1 ? gib([...totals][0]) : '';
  });
  const badBudget = $derived(memory.trim() !== '' && budget === 0n);
  const badName = $derived(/[\s/]/.test(name));
  const nameTaken = $derived(name.trim() !== slot?.name && [...live.slots.values()].some((s) => s.name === name.trim()));
  const runtime = $derived(cached.runtimes.find((r) => r.runtime?.id === runtimeId)?.runtime);
  const occupied = $derived(!!slot && slotOccupied(slot.id));
  const count = $derived(orderedSlots().length);
  const formOk = $derived(!!name.trim() && !badName && !nameTaken && !badBudget && invalid === 0);
  // Clear parameters when the runtime changes.
  $effect(() => {
    if (runtimeId !== start.runtimeId) params = {};
  });

  async function save() {
    saving = true;
    const body = { placement: placementOf(placement), deviceIds, memoryBytes: budget, runtimeId, params, policy: policyFrom(policy), profile: profileFrom(profile), position: Math.max(0, parseInt(position, 10) || 0) };
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
  <Card title="Slot">
    <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-[minmax(0,1fr)_8rem]">
      <Field label="Name" for="slot-name" required error={badName ? 'No spaces or slashes' : nameTaken ? 'Another slot has this name' : undefined}>
        <TextInput id="slot-name" mono bind:value={name} empty="main" invalid={badName || nameTaken} />
      </Field>
      <Field label="Position" for="slot-position">
        <NumberInput id="slot-position" integer min={1} max={count + (creating ? 1 : 0)} bind:value={position} empty={creating ? String(count + 1) : ''} />
      </Field>
    </div>
  </Card>

  <Card title="Memory" meta={occupied ? 'Applies to the next run' : undefined}>
    <div class="flex flex-col gap-4">
      <Field label="Placement" for="slot-placement">
        <div id="slot-placement"><Segmented size="lg" bind:value={placement} tabs={placementTabs} /></div>
      </Field>
      {#if pickDevices}
        <Field label="GPUs" for="slot-devices">
          <DevicePicker id="slot-devices" bind:value={() => chosen, (v) => (devices = v)} />
        </Field>
      {/if}
      <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
        <Field label={hostOnly ? 'RAM cap' : 'Cap per GPU'} for="slot-memory" error={badBudget ? 'A number of GiB, such as 8' : undefined}>
          <NumberInput id="slot-memory" min={0} step={0.5} unit="GiB" bind:value={memory} empty={poolGib} invalid={badBudget} />
        </Field>
        <Field label="Runtime" for="slot-runtime">
          <Select id="slot-runtime" bind:value={runtimeId} items={[{ value: '', label: 'Any', detail: 'the first that reads the model' }, ...cached.runtimes.map((rt) => ({ value: rt.runtime?.id ?? '', label: rt.runtime?.name ?? rt.runtime?.id ?? '', detail: rt.compatible ? undefined : 'not compatible with this host', disabled: !rt.compatible }))]} />
        </Field>
      </div>
    </div>
  </Card>

  <Card title="Parameters" meta={runtime?.name}>
    {#if runtime}
      <ParamForm params={runtime.params} bind:values={params} bind:invalid idPrefix="slot" />
    {:else}
      <p class="text-sm text-fg-faint">Pick a runtime first</p>
    {/if}
  </Card>

  <Card title="Limits">
    <PolicyForm bind:fields={policy} defaults={cached.gateway?.policy} idPrefix="slot" />
  </Card>

  <Card title="Shaping">
    <ProfileForm bind:value={profile} idPrefix="slot" />
  </Card>

  <div class="flex items-center justify-end gap-2">
    <Button variant="ghost" href={cancelHref}>Cancel</Button>
    <Button type="submit" variant="primary" loading={saving} disabled={!formOk}>{creating ? 'Create slot' : 'Save'}</Button>
  </div>
</form>
