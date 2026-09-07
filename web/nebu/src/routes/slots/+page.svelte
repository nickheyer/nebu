<script lang="ts">
  import { afterNavigate, goto } from '$app/navigation';

  // Slots are listed on the serve page
  afterNavigate(() => {
    goto('/', { replaceState: true });
  });
</script>
