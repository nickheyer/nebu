<script lang="ts">
  import { live } from '$lib/state.svelte';
  import { bytes } from '$lib/format';
  import { DeviceKind } from '$proto/host_pb';

  // The accelerators a slot is pinned to, every one when none is picked
  let { value = $bindable([] as string[]), id = 'devices' }: { value?: string[]; id?: string } = $props();

  const gpus = $derived((live.host?.devices ?? []).filter((d) => d.kind !== DeviceKind.CPU));

  function toggle(device: string) {
    value = value.includes(device) ? value.filter((d) => d !== device) : [...value, device];
  }
</script>

<div {id} class="divide-y divide-line rounded-md border border-line bg-sunken/40">
  {#if gpus.length === 0}
    <div class="px-3 py-3 text-sm text-fg-faint">No accelerators were probed on this host.</div>
  {:else}
    <label class="flex h-10 cursor-pointer items-center gap-3 px-3 text-sm">
      <input type="checkbox" class="checkbox" checked={value.length === 0} disabled={value.length === 0} onchange={() => (value = [])} />
      <span class="flex-1 text-fg">All devices</span>
      <span class="text-xs text-fg-faint">{gpus.length} probed</span>
    </label>
    {#each gpus as d (d.id)}
      <label class="flex h-10 cursor-pointer items-center gap-3 px-3 text-sm">
        <input type="checkbox" class="checkbox" checked={value.includes(d.id)} onchange={() => toggle(d.id)} />
        <span class="min-w-0 flex-1 truncate text-fg">{d.name || d.id}</span>
        <span class="font-mono text-xs text-fg-faint">{d.id}</span>
        <span class="w-16 text-right text-xs tabular-nums text-fg-muted">{bytes(d.memoryTotalBytes, 0)}</span>
      </label>
    {/each}
  {/if}
</div>
