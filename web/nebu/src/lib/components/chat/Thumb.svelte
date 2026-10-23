<script lang="ts">
  import { ImageOff } from '@lucide/svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';
  import type { Attachment } from '$lib/images';

  let { a, size, url, missing, onopen }: { a: Attachment; size: string; url: string | undefined; missing: boolean; onopen: (url: string | undefined, alt: string) => void } = $props();
</script>

{#if missing}
  <div class="flex {size} flex-col items-center justify-center gap-1 rounded-md border border-dashed border-line text-xs text-fg-faint" title="{a.name} could not be loaded"><ImageOff size={16} />image missing</div>
{:else if url}
  <button type="button" class="block overflow-hidden rounded-md border border-line" onclick={() => onopen(url, a.name)} aria-label="Open {a.name}">
    <img src={url} alt={a.name} width={a.width} height={a.height} class="block h-auto max-h-64 w-auto max-w-full object-contain" />
  </button>
{:else}
  <div class="flex {size} items-center justify-center rounded-md border border-line"><Spinner size={14} class="text-fg-faint" /></div>
{/if}
