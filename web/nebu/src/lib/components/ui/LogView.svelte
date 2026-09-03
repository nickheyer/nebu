<script lang="ts">
  import { ArrowDownToLine, WrapText, Search } from '@lucide/svelte';
  import Copy from './Copy.svelte';

  let {
    lines,
    height = 'h-80',
    live = false,
    empty = 'Nothing yet'
  }: { lines: string[]; height?: string; live?: boolean; empty?: string } = $props();

  let box: HTMLDivElement | undefined = $state();
  let follow = $state(true);
  let wrap = $state(false);
  let filter = $state('');

  const shown = $derived(filter ? lines.filter((l) => l.toLowerCase().includes(filter.toLowerCase())) : lines);

  $effect(() => {
    // Track the line count so a change scrolls when following
    void shown.length;
    if (follow && box) requestAnimationFrame(() => box?.scrollTo({ top: box.scrollHeight }));
  });

  function onScroll() {
    if (!box) return;
    const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 24;
    follow = atBottom;
  }
</script>

<div class="flex flex-col overflow-hidden rounded-lg border border-line bg-sunken">
  <div class="flex items-center gap-2 border-b border-line px-2 py-1.5">
    <div class="relative flex-1">
      <Search size={12} class="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-fg-faint" />
      <input class="h-6 w-full rounded border border-transparent bg-transparent pl-6 text-xs text-fg placeholder:text-fg-faint focus:border-line focus:outline-none" placeholder="Filter lines" bind:value={filter} />
    </div>
    <span class="text-[11px] tabular-nums text-fg-faint">{shown.length}{filter ? ` / ${lines.length}` : ''} lines</span>
    {#if live}<span class="flex items-center gap-1 text-[11px] text-ok"><span class="relative inline-block h-1.5 w-1.5 rounded-full bg-ok pulse"></span>live</span>{/if}
    <button class="rounded p-1 text-fg-faint hover:bg-raised hover:text-fg {wrap ? 'bg-raised text-fg' : ''}" title="Wrap lines" onclick={() => (wrap = !wrap)}><WrapText size={13} /></button>
    <button
      class="rounded p-1 text-fg-faint hover:bg-raised hover:text-fg {follow ? 'bg-raised text-fg' : ''}"
      title="Follow output"
      onclick={() => {
        follow = true;
        box?.scrollTo({ top: box.scrollHeight });
      }}><ArrowDownToLine size={13} /></button
    >
    <Copy text={lines.join('\n')} label="Copy log" size={13} />
  </div>
  <div bind:this={box} onscroll={onScroll} class="{height} overflow-auto p-3 font-mono text-[12px] leading-5 text-fg-muted {wrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre'}">
    {#if shown.length === 0}
      <span class="text-fg-faint">{empty}</span>
    {:else}
      {#each shown as line, i (i)}<div class="hover:text-fg">{line}</div>{/each}
    {/if}
  </div>
</div>
