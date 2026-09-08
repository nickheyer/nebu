<script lang="ts">
  import { goto } from '$app/navigation';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import SlotForm from '$lib/components/SlotForm.svelte';
</script>

<PageHeader title="New slot" back={{ href: '/', label: 'Serve' }}>
  {#snippet meta()}
    <span>A reserved place with a fixed model name. Swap models in and out without clients noticing.</span>
  {/snippet}
</PageHeader>

<SlotForm cancelHref="/" onSaved={(s) => goto(`/slots/${s.id}`)} />
