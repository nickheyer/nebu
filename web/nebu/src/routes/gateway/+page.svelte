<script lang="ts">
    import { afterNavigate, goto } from '$app/navigation';

  // The gateway is shown from the serve page's connect dialog now
  afterNavigate(() => {
    goto('/?connect=1', { replaceState: true });
  });
</script>
