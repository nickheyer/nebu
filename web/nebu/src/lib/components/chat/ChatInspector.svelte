<script lang="ts">
  import type { Snippet } from 'svelte';
  import { readLocal, writeLocal } from '$lib/persist';
  import Tabs, { type Tab } from '$lib/components/ui/Tabs.svelte';

  // Flush drops the card frame so the panel sits against the window edge in theater mode.
  let { pane = $bindable(''), tabs, padded, flush = false, children }: { pane?: string; tabs: Tab[]; padded: boolean; flush?: boolean; children: Snippet } = $props();

  const widthKey = 'nebu.chat.inspector';
  const minWidth = 320;
  let width = $state(384);
  let dragging = $state(false);
  const savedWidth = parseInt(readLocal(widthKey), 10);
  if (savedWidth >= minWidth) width = clampWidth(savedWidth);

  function clampWidth(w: number): number {
    return Math.min(Math.max(w, minWidth), Math.max(window.innerWidth * 0.6, minWidth));
  }

  function startDrag(e: PointerEvent) {
    e.preventDefault();
    dragging = true;
    const target = e.currentTarget as HTMLElement;
    const startX = e.clientX;
    const startWidth = width;
    target.setPointerCapture(e.pointerId);
    const move = (ev: PointerEvent) => {
      width = clampWidth(startWidth + (startX - ev.clientX));
    };
    const stop = () => {
      dragging = false;
      target.releasePointerCapture(e.pointerId);
      target.removeEventListener('pointermove', move);
      target.removeEventListener('pointerup', stop);
      target.removeEventListener('pointercancel', stop);
      writeLocal(widthKey, String(Math.round(width)));
    };
    target.addEventListener('pointermove', move);
    target.addEventListener('pointerup', stop);
    target.addEventListener('pointercancel', stop);
  }
</script>

<aside class="relative flex shrink-0 flex-col {flush ? 'border-l border-line bg-bg' : 'card'} {dragging ? 'select-none' : ''}" style="width: {width}px">
  <div role="separator" aria-orientation="vertical" aria-label="Resize" class="group absolute inset-y-0 -left-1 z-10 w-2 cursor-col-resize" onpointerdown={startDrag}>
    <div class="mx-auto h-full w-px bg-transparent transition-colors group-hover:bg-accent/60 {dragging ? 'bg-accent' : ''}"></div>
  </div>
  <Tabs size="sm" bind:value={pane} {tabs} class="px-3 pt-1" />
  <div class="@container min-h-0 flex-1 overflow-y-auto {padded ? 'p-4' : 'p-2'}">
    {@render children()}
  </div>
</aside>
