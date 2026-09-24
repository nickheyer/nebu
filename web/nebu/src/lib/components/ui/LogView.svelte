<script lang="ts">
  import { ArrowDownToLine, TextWrap, Search } from '@lucide/svelte';
  import Copy from './Copy.svelte';
  import IconButton from './IconButton.svelte';

  let {
    lines,
    height = 'h-80',
    live = false,
    empty = 'No output'
  }: { lines: string[]; height?: string; live?: boolean; empty?: string } = $props();

  let box: HTMLDivElement | undefined = $state();
  let follow = $state(true);
  let wrap = $state(false);
  let filter = $state('');

  const shown = $derived(filter ? lines.filter((l) => l.toLowerCase().includes(filter.toLowerCase())) : lines);

  $effect(() => {
    void shown.length;
    if (follow && box) requestAnimationFrame(() => box?.scrollTo({ top: box.scrollHeight }));
  });

  function onScroll() {
    if (!box) return;
    follow = box.scrollHeight - box.scrollTop - box.clientHeight < 24;
  }
</script>

<div class="flex flex-col overflow-hidden rounded-md border border-line bg-sunken">
  <div class="flex items-center gap-1 border-b border-line px-1.5 py-1">
    <div class="relative flex-1">
      <Search size={13} class="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-fg-faint" />
      <input class="h-7 w-full rounded-md border border-transparent bg-transparent pl-7 text-sm text-fg placeholder:text-fg-faint focus:border-line focus:outline-none" aria-label="Filter" bind:value={filter} />
    </div>
    <span class="px-1 text-xs tabular-nums text-fg-faint">{shown.length}{filter ? ` / ${lines.length}` : ''}</span>
    {#if live}<span class="mx-1 inline-flex items-center gap-1.5 text-xs text-ok"><span class="dot pulse"></span>live</span>{/if}
    <IconButton size="xs" icon={TextWrap} label="Wrap lines" class={wrap ? 'bg-raised text-fg' : ''} onclick={() => (wrap = !wrap)} />
    <IconButton
      size="xs"
      icon={ArrowDownToLine}
      label="Follow output"
      class={follow ? 'bg-raised text-fg' : ''}
      onclick={() => {
        follow = true;
        box?.scrollTo({ top: box.scrollHeight });
      }}
    />
    <Copy text={lines.join('\n')} label="Copy log" size={13} />
  </div>
  <div bind:this={box} onscroll={onScroll} class="{height} overflow-auto p-3 font-mono text-xs leading-5 text-fg-muted {wrap ? 'whitespace-pre-wrap wrap-anywhere' : 'whitespace-pre'}">
    {#if shown.length === 0}
      <span class="text-fg-faint">{empty}</span>
    {:else}
      {#each shown as line, i (i)}<div class="hover:text-fg">{line}</div>{/each}
    {/if}
  </div>
</div>
