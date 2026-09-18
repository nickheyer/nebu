<script lang="ts">
  import { goto } from '$app/navigation';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import BotForm from '$lib/components/bots/BotForm.svelte';
</script>

<PageHeader title="New bot" back={{ href: '/bots', label: 'Bots' }} />

<BotForm section="settings" cancelHref="/bots" onSaved={(b) => goto(`/bots/${b.id}`)} />
