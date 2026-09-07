<script lang="ts">
  import { afterNavigate, goto } from '$app/navigation';

  // The gateway is shown on the serve page now
  afterNavigate(() => {
    goto('/', { replaceState: true });
  });
</script>
