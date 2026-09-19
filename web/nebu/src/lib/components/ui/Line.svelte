<script lang="ts">
  import type { Segment } from '$lib/format';

  let { segments }: { segments: Segment[] } = $props();
</script>

{#each segments as s, i (i)}{#if s.mono}<span class="font-mono whitespace-nowrap">{s.text}</span>{:else}{s.text}{/if}{/each}
