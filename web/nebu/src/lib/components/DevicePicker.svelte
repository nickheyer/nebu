<script lang="ts">
  import { hostGpus, type DeviceGroup } from '$lib/state.svelte';
  import DeviceRows from './DeviceRows.svelte';

  // Picks the accelerators a slot pins, showing the memory each holds right now
  let {
    value = $bindable([] as string[]),
    id = 'devices',
    groups,
    cap = 0n,
    exceptSlot = ''
  }: { value?: string[]; id?: string; groups?: DeviceGroup[]; cap?: bigint; exceptSlot?: string } = $props();

  const shown = $derived<DeviceGroup[]>(groups ?? [{ nodeId: '', node: '', self: true, ready: true, devices: hostGpus() }]);
  const all = $derived(shown.flatMap((g) => g.devices));

  function toggle(device: string, on: boolean) {
    const next = new Set(value);
    if (on) next.add(device);
    else next.delete(device);
    value = all.filter((d) => next.has(d.id)).map((d) => d.id);
  }
</script>

<div {id}>
  <DeviceRows groups={shown} {cap} {exceptSlot} highlight="this slot">
    {#snippet lead(d)}
      <input type="checkbox" class="checkbox shrink-0" aria-label="Use {d.name || d.id}" checked={value.includes(d.id)} onchange={(e) => toggle(d.id, e.currentTarget.checked)} />
    {/snippet}
  </DeviceRows>
</div>
