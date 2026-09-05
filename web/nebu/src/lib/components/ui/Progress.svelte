<script lang="ts">
  import { pct } from '$lib/format';

  let {
    done,
    total,
    active = false,
    tone = 'accent',
    class: cls = ''
  }: { done?: bigint | number; total?: bigint | number; active?: boolean; tone?: 'accent' | 'ok' | 'bad' | 'warn'; class?: string } = $props();

  const fills = { accent: 'bg-accent', ok: 'bg-ok', bad: 'bg-bad', warn: 'bg-warn' };
  const known = $derived(!!total && Number(total) > 0);
</script>

<div class="relative h-1.5 w-full overflow-hidden rounded-full bg-line/70 {cls}">
  {#if known}
    <div class="h-full rounded-full {fills[tone]} transition-[width] duration-300" style="width: {pct(done, total)}%"></div>
  {:else if active}
    <div class="indeterminate absolute top-0 h-full w-2/5 rounded-full {fills[tone]} opacity-80"></div>
  {/if}
</div>
