<script lang="ts">
  // name=value pairs in mono, the names faint, folded past a limit
  let { params, max = Infinity, class: cls = '' }: { params: Record<string, string>; max?: number; class?: string } = $props();
  const entries = $derived(Object.entries(params));
</script>

<span class="inline-flex max-w-full flex-wrap items-center gap-x-3 gap-y-1 font-mono text-xs {cls}">
  {#each entries.slice(0, max) as [k, v] (k)}
    <span class="inline-flex max-w-full items-baseline whitespace-nowrap" title="{k}={v}"><span class="text-fg-faint">{k}=</span><span class="truncate text-fg">{v.length > 32 ? v.slice(0, 32) + '…' : v}</span></span>
  {/each}
  {#if entries.length > max}<span class="text-fg-faint">+{entries.length - max}</span>{/if}
</span>
