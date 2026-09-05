<script lang="ts">
  import { live } from '$lib/state.svelte';
  import { dnd, acceptsModel } from '$lib/dnd.svelte';
  import { dropModelOnSlot, slotOccupied } from '$lib/launch';
  import { byName } from '$lib/format';
  import { SlotState } from '$proto/slot_pb';
  import { LayoutGrid, Plus } from '@lucide/svelte';
  import StateBadge from './ui/StateBadge.svelte';
  import Button from './ui/Button.svelte';

  const slots = $derived([...live.slots.values()].sort(byName((s) => s.name)));
  let over = $state('');
</script>

<aside class="panel sticky top-0 flex flex-col gap-2 p-3">
  <div class="flex items-center gap-2 px-1 text-xs font-semibold tracking-wider text-fg-faint uppercase">
    <LayoutGrid size={12} /> Slots
  </div>
  {#if slots.length === 0}
    <p class="px-1 text-xs leading-5 text-fg-faint">No slots</p>
    <Button size="sm" href="/slots" icon={Plus}>New slot</Button>
  {:else}
    <p class="px-1 text-xs leading-5 text-fg-faint">Drop a model on a slot to run it there</p>
    {#each slots as s (s.id)}
      {@const occupied = slotOccupied(s.id)}
      <div
        role="group"
        class="drop-target rounded-md border border-line bg-sunken px-3 py-2 transition-colors"
        data-armed={!!dnd.model}
        data-over={over === s.id}
        ondragover={(e) => {
          if (!acceptsModel(e)) return;
          e.preventDefault();
          over = s.id;
        }}
        ondragleave={() => (over = '')}
        ondrop={(e) => {
          over = '';
          dropModelOnSlot(e, s.id);
        }}
      >
        <div class="flex items-center justify-between gap-2">
          <a href="/slots?id={s.id}" class="truncate text-sm font-medium text-fg hover:underline">{s.name}</a>
          <StateBadge values={SlotState} value={s.state} size="xs" />
        </div>
        <div class="mt-0.5 truncate font-mono text-[11px] text-fg-faint">
          {#if over === s.id}
            <span class="text-accent">{occupied ? 'drop to swap' : 'drop to run'}</span>
          {:else if s.request?.repo}
            {s.request.repo} · {s.request.group}
          {:else}
            empty
          {/if}
        </div>
      </div>
    {/each}
  {/if}
</aside>
