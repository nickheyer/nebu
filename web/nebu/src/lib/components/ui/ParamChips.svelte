<script lang="ts">
  let { params, max = Infinity, class: cls = '' }: { params: Record<string, string>; max?: number; class?: string } = $props();
  const entries = $derived(Object.entries(params));
</script>

<div class="flex flex-wrap items-center gap-1.5 {cls}">
  {#each entries.slice(0, max) as [k, v] (k)}
    <span class="rounded border border-line bg-sunken px-1.5 py-0.5 font-mono text-[11px]" title="{k}={v}"><span class="text-fg-faint">{k}=</span><span class="text-fg">{v.length > 32 ? v.slice(0, 32) + '…' : v}</span></span>
  {/each}
  {#if entries.length > max}<span class="text-[11px] text-fg-faint">+{entries.length - max}</span>{/if}
</div>
