<script lang="ts">
  import { live, liveInstances } from '$lib/state.svelte';
  import { selectionParam } from '$lib/selection.svelte';
  import { newSlot } from '$lib/slotActions.svelte';
  import { byName } from '$lib/format';
  import { SlotState } from '$proto/slot_pb';
  import { Plus, LayoutGrid } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import SlotCard from '$lib/components/SlotCard.svelte';
  import SlotDrawer from '$lib/components/SlotDrawer.svelte';
  import InstanceDrawer from '$lib/components/InstanceDrawer.svelte';

  const sel = selectionParam('/slots');
  let instanceId = $state('');

  const slots = $derived([...live.slots.values()].sort(byName((s) => s.name)));
  const serving = $derived(slots.filter((s) => s.state === SlotState.READY).length);
  const standalone = $derived(liveInstances().filter((i) => !i.slotId).length);
</script>

<PageHeader title="Slots" description="Reservations of devices and memory under public names that never disappear">
  {#snippet meta()}
    <span>{slots.length} {slots.length === 1 ? 'slot' : 'slots'}</span>
    <span>·</span>
    <span>{serving} serving</span>
    {#if standalone}
      <span>·</span>
      <a href="/instances" class="hover:text-fg">{standalone} running outside slots</a>
    {/if}
  {/snippet}
  <Button variant="primary" icon={Plus} onclick={newSlot}>New slot</Button>
</PageHeader>

{#if slots.length === 0}
  <div class="panel">
    <Empty icon={LayoutGrid} title="No slots yet" description="A slot pins a run to chosen devices and a memory budget, and gives it a public name. Swap what it serves without your router noticing. Empty slots answer 503 with a retry hint rather than 404.">
      <Button variant="primary" icon={Plus} onclick={newSlot}>Create a slot</Button>
    </Empty>
  </div>
{:else}
  <div class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
    {#each slots as s (s.id)}
      <SlotCard slot={s} onOpen={(x) => (sel.id = x.id)} />
    {/each}
  </div>
  <p class="mt-4 text-xs text-fg-faint">Drag a model from the store onto a slot to serve it there. Dropping on an occupied slot swaps.</p>
{/if}

<SlotDrawer bind:id={sel.id} onInstance={(i) => (instanceId = i)} />
<InstanceDrawer bind:id={instanceId} />
