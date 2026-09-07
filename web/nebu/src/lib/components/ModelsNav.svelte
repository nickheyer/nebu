<script lang="ts">
  import { page } from '$app/state';
  import { live } from '$lib/state.svelte';
  import Tabs from './ui/Tabs.svelte';

  // The two halves of the models page: what is downloaded and what the sources offer
  const value = $derived(page.url.pathname.startsWith('/catalog') ? 'discover' : 'library');
</script>

<Tabs
  {value}
  tabs={[
    { id: 'library', label: 'Library', href: '/store', count: live.models.size || undefined },
    { id: 'discover', label: 'Catalog', href: '/catalog' }
  ]}
/>
