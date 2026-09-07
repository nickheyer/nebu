<script lang="ts">
  import { afterNavigate, goto } from '$app/navigation';

  // Instances are listed on the serve page
  afterNavigate(() => {
    goto('/', { replaceState: true });
  });
</script>
