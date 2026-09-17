<script lang="ts">
  import { hostGpus } from '$lib/state.svelte';
  import { bytes } from '$lib/format';

  // The accelerators a slot is placed on, every one checked to start; the last one checked stays checked
  let { value = $bindable([] as string[]), id = 'devices' }: { value?: string[]; id?: string } = $props();

  const gpus = $derived(hostGpus());

  function toggle(device: string, on: boolean) {
    const next = new Set(value);
    if (on) next.add(device);
    else next.delete(device);
    value = gpus.filter((d) => next.has(d.id)).map((d) => d.id);
  }
</script>

<div {id} class="divide-y divide-line rounded-md border border-line bg-sunken/40">
  {#each gpus as d (d.id)}
    {@const on = value.includes(d.id)}
    <label class="flex h-10 cursor-pointer items-center gap-3 px-3 text-sm">
      <input type="checkbox" class="checkbox" checked={on} disabled={on && value.length === 1} onchange={(e) => toggle(d.id, e.currentTarget.checked)} />
      <span class="min-w-0 flex-1 truncate text-fg">{d.name || d.id}</span>
      <span class="font-mono text-xs text-fg-faint">{d.id}</span>
      <span class="w-16 text-right text-xs tabular-nums text-fg-muted">{bytes(d.memoryTotalBytes, 0)}</span>
    </label>
  {/each}
</div>
