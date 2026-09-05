<script lang="ts">
  import { live, liveInstances } from '$lib/state.svelte';
  import { selectionParam } from '$lib/selection.svelte';
  import { newSlot } from '$lib/slotActions.svelte';
  import { byName } from '$lib/format';
  import { SlotState } from '$proto/slot_pb';
  import { Plus } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Info from '$lib/components/ui/Info.svelte';
  import SlotBay from '$lib/components/SlotBay.svelte';
  import SlotDrawer from '$lib/components/SlotDrawer.svelte';
  import InstanceDrawer from '$lib/components/InstanceDrawer.svelte';

  const sel = selectionParam('/slots');
  let instanceId = $state('');

  const slots = $derived([...live.slots.values()].sort(byName((s) => s.name)));
  const serving = $derived(slots.filter((s) => s.state === SlotState.READY).length);
  const standalone = $derived(liveInstances().filter((i) => !i.slotId).length);
</script>

<PageHeader title="Slots">
  {#snippet meta()}
    <span>{slots.length} {slots.length === 1 ? 'slot' : 'slots'}</span>
    <span>·</span>
    <span>{serving} serving</span>
    {#if standalone}
      <span>·</span>
      <a href="/instances" class="hover:text-fg">{standalone} running outside slots</a>
    {/if}
    <Info text="A slot holds a public model name on chosen devices. Drop a stored model on it to run or swap." />
  {/snippet}
  <Button variant="primary" icon={Plus} onclick={newSlot}>New slot</Button>
</PageHeader>

<div class="flex flex-col gap-2">
  {#each slots as s (s.id)}
    <SlotBay slot={s} onOpen={(x) => (sel.id = x.id)} />
  {:else}
    <div class="flex h-24 items-center justify-center rounded-lg border border-dashed border-line text-sm text-fg-faint">No slots</div>
  {/each}
</div>

<SlotDrawer bind:id={sel.id} onInstance={(i) => (instanceId = i)} />
<InstanceDrawer bind:id={instanceId} />
