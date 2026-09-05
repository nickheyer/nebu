<script lang="ts">
    import { page } from '$app/state';
  import { afterNavigate, goto } from '$app/navigation';

  // Instances live on the serve page now, so an old link opens the same instance there
  afterNavigate(() => {
    const id = page.url.searchParams.get('id');
    goto(id ? `/?instance=${encodeURIComponent(id)}` : '/', { replaceState: true });
  });
</script>
