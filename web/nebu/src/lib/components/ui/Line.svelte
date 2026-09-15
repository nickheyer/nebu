<script lang="ts">
  import type { Segment } from '$lib/format';

  // Words with the identifiers among them set in mono and kept whole across line breaks
  let { segments }: { segments: Segment[] } = $props();
</script>

{#each segments as s, i (i)}{#if s.mono}<span class="font-mono whitespace-nowrap">{s.text}</span>{:else}{s.text}{/if}{/each}
