<script lang="ts">
  // name=value pairs as mono chips, the names faint
  let { params, max = Infinity, class: cls = '' }: { params: Record<string, string>; max?: number; class?: string } = $props();
  const entries = $derived(Object.entries(params));
</script>

<span class="inline-flex max-w-full flex-wrap items-center gap-1.5 {cls}">
  {#each entries.slice(0, max) as [k, v] (k)}
    <span class="inline-flex h-6 max-w-full items-center rounded-md border border-line bg-sunken px-2 font-mono text-xs whitespace-nowrap" title="{k}={v}"><span class="text-fg-faint">{k}=</span><span class="truncate text-fg">{v.length > 32 ? v.slice(0, 32) + '…' : v}</span></span>
  {/each}
  {#if entries.length > max}<span class="text-xs text-fg-faint">+{entries.length - max}</span>{/if}
</span>
