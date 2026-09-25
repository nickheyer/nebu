<script lang="ts">
  import { hostGpus, type DeviceGroup } from '$lib/state.svelte';
  import { bytes } from '$lib/format';
  import type { Device } from '$proto/host_pb';

  // Keep at least one device selected. Devices default to this host's accelerators; a slot that
  // spans the mesh passes every member's devices under their nodes, with node qualified ids.
  let { value = $bindable([] as string[]), id = 'devices', groups }: { value?: string[]; id?: string; groups?: DeviceGroup[] } = $props();

  const shown = $derived<DeviceGroup[]>(groups ?? [{ nodeId: '', node: '', self: true, ready: true, devices: hostGpus() }]);
  const all = $derived(shown.flatMap((g) => g.devices));

  function toggle(device: string, on: boolean) {
    const next = new Set(value);
    if (on) next.add(device);
    else next.delete(device);
    value = all.filter((d) => next.has(d.id)).map((d) => d.id);
  }

  function row(d: Device, disabledNode: boolean) {
    const on = value.includes(d.id);
    return { on, disabled: (on && value.length === 1) || (!on && disabledNode) };
  }
</script>

<div {id} class="divide-y divide-line overflow-hidden rounded-md border border-line bg-sunken/40">
  {#each shown as g (g.nodeId)}
    {#if g.node}
      <div class="flex items-center gap-2 bg-sunken/60 px-3 py-2 text-xs font-medium text-fg-muted">
        <span class="min-w-0 flex-1 truncate">{g.node}</span>
        {#if !g.ready}<span class="shrink-0 text-warn">Unreachable</span>{/if}
        <span class="shrink-0 font-normal text-fg-faint">{g.devices.length} {g.devices.length === 1 ? 'GPU' : 'GPUs'}</span>
      </div>
    {/if}
    {#each g.devices as d (d.id)}
      {@const r = row(d, !g.ready)}
      {@const displayId = g.nodeId && d.id.startsWith(`${g.nodeId}/`) ? d.id.slice(g.nodeId.length + 1) : d.id}
      <label class="flex h-10 items-center gap-3 px-3 text-sm {g.node ? 'pl-6' : ''} {r.disabled ? 'cursor-default' : 'cursor-pointer hover:bg-raised/30'}">
        <input type="checkbox" class="checkbox shrink-0" checked={r.on} disabled={r.disabled} onchange={(e) => toggle(d.id, e.currentTarget.checked)} />
        <span class="min-w-0 flex-1 truncate text-fg" title={d.name || displayId}>{d.name || displayId}</span>
        {#if d.name}<span class="max-w-24 truncate font-mono text-xs text-fg-faint" title={d.id}>{displayId}</span>{/if}
        <span class="w-16 shrink-0 text-right text-xs tabular-nums text-fg-muted">{bytes(d.memoryTotalBytes, 0)}</span>
      </label>
    {/each}
  {/each}
</div>
