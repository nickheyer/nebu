<script lang="ts">
  import { afterNavigate, goto } from '$app/navigation';

  afterNavigate(() => {
    goto('/', { replaceState: true });
  });
</script>
