<script lang="ts">
  import { prettyJson } from '$lib/format';
  import Copy from './Copy.svelte';

  let { text, height = 'max-h-96', empty = 'Empty' }: { text: string; height?: string; empty?: string } = $props();
  const shown = $derived(prettyJson(text));
</script>

{#if !text}
  <p class="text-sm text-fg-faint">{empty}</p>
{:else}
  <div class="relative">
    <pre class="code {height} overflow-auto whitespace-pre-wrap wrap-anywhere pr-10">{shown}</pre>
    <div class="absolute top-1.5 right-1.5"><Copy text={shown} /></div>
  </div>
{/if}
