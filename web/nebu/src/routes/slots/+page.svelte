<script lang="ts">
    import { page } from '$app/state';
  import { afterNavigate, goto } from '$app/navigation';

  // Slots live on the serve page now, so an old link opens the same slot there
  afterNavigate(() => {
    const id = page.url.searchParams.get('id');
    goto(id ? `/?slot=${encodeURIComponent(id)}` : '/', { replaceState: true });
  });
</script>
