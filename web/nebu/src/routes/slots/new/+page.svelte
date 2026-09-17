<script lang="ts">
  import { goto } from '$app/navigation';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import SlotForm from '$lib/components/SlotForm.svelte';
</script>

<PageHeader title="New slot" back={{ href: '/', label: 'Serve' }} />

<SlotForm cancelHref="/" onSaved={(s) => goto(`/slots/${s.id}`)} />
