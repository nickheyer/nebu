<script lang="ts">
  import { slotUi } from '$lib/slotActions.svelte';
  import RunDialog from './RunDialog.svelte';
  import SlotDialog from './SlotDialog.svelte';
</script>

<RunDialog bind:open={slotUi.runOpen} model={slotUi.runModel} slotId={slotUi.runSlotId} />
<SlotDialog bind:open={slotUi.editOpen} slot={slotUi.editSlot} />
