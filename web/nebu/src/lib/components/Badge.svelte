<script lang="ts">
  import { tone } from '$lib/format';
  let { state }: { state: string } = $props();
</script>

<span class="badge {tone(state)}">{state}</span>
