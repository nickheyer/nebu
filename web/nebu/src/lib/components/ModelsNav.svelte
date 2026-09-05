<script lang="ts">
  import { page } from '$app/state';
  import { live } from '$lib/state.svelte';
  import Segmented from './ui/Segmented.svelte';

  // The two halves of the models page: what this host holds and what the sources offer
  const value = $derived(page.url.pathname.startsWith('/catalog') ? 'discover' : 'library');
</script>

<Segmented
  {value}
  tabs={[
    { id: 'library', label: 'Library', href: '/store', count: live.models.size || undefined },
    { id: 'discover', label: 'Discover', href: '/catalog' }
  ]}
/>
