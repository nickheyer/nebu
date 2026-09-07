<script lang="ts">
  import { live } from '$lib/state.svelte';
  import { bytes } from '$lib/format';
  import { DeviceKind } from '$proto/host_pb';
  import { Check } from '@lucide/svelte';
  import Segmented from './ui/Segmented.svelte';

  // The accelerators a slot is pinned to: any device while none is picked, else only the ones checked
  let { value = $bindable([] as string[]), id = 'devices' }: { value?: string[]; id?: string } = $props();

  const gpus = $derived((live.host?.devices ?? []).filter((d) => d.kind !== DeviceKind.CPU));
  const some = $derived(value.length > 0);

  function pick(mode: string) {
    if (mode === 'some') {
      if (!value.length) value = gpus.map((d) => d.id);
    } else {
      value = [];
    }
  }

  function toggle(device: string) {
    value = value.includes(device) ? value.filter((d) => d !== device) : [...value, device];
  }
</script>

<div {id} class="flex flex-col gap-2">
  {#if gpus.length === 0}
    <div class="rounded-md border border-line bg-sunken/40 px-3 py-3 text-sm text-fg-faint">No accelerators were probed on this host.</div>
  {:else}
    <div><Segmented bind:value={() => (some ? 'some' : 'any'), pick} tabs={[{ id: 'any', label: 'Any device' }, { id: 'some', label: 'Only these' }]} /></div>
    <div class="divide-y divide-line rounded-md border border-line bg-sunken/40">
      {#each gpus as d (d.id)}
        {#if some}
          <label class="flex h-10 cursor-pointer items-center gap-3 px-3 text-sm">
            <input type="checkbox" class="checkbox" checked={value.includes(d.id)} onchange={() => toggle(d.id)} />
            <span class="min-w-0 flex-1 truncate text-fg">{d.name || d.id}</span>
            <span class="font-mono text-xs text-fg-faint">{d.id}</span>
            <span class="w-16 text-right text-xs tabular-nums text-fg-muted">{bytes(d.memoryTotalBytes, 0)}</span>
          </label>
        {:else}
          <div class="flex h-10 items-center gap-3 px-3 text-sm">
            <Check size={14} class="shrink-0 text-fg-faint" aria-hidden="true" />
            <span class="min-w-0 flex-1 truncate text-fg-muted">{d.name || d.id}</span>
            <span class="font-mono text-xs text-fg-faint">{d.id}</span>
            <span class="w-16 text-right text-xs tabular-nums text-fg-faint">{bytes(d.memoryTotalBytes, 0)}</span>
          </div>
        {/if}
      {/each}
    </div>
  {/if}
</div>
