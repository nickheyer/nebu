<script lang="ts">
  import { Dialog } from 'bits-ui';
  import { X } from '@lucide/svelte';
  import type { Snippet } from 'svelte';

  let {
    open = $bindable(false),
    id = $bindable(''),
    title,
    subtitle,
    header,
    children,
    footer
  }: {
    open?: boolean;
    // Bound instead of open by drawers showing one record, empty when closed
    id?: string;
    title: string;
    subtitle?: string;
    header?: Snippet;
    children: Snippet;
    footer?: Snippet;
  } = $props();

  // Every drawer opens at the same share of the window, kept per browser once dragged
  const widthKey = 'nebu.drawer.width';
  const defaultShare = 0.42;
  const minWidth = 420;
  const minRemaining = 200;

  let share = $state(defaultShare);
  let dragging = $state(false);

  function readShare() {
    try {
      const v = parseFloat(localStorage.getItem(widthKey) ?? '');
      if (v >= 0.2 && v <= 0.95) share = v;
    } catch {
      // storage may be unavailable
    }
  }
  readShare();

  function px(): number {
    if (typeof window === 'undefined') return 0;
    const w = window.innerWidth;
    return Math.min(Math.max(w * share, minWidth), Math.max(w - minRemaining, minWidth));
  }

  function startDrag(e: PointerEvent) {
    e.preventDefault();
    dragging = true;
    const target = e.currentTarget as HTMLElement;
    target.setPointerCapture(e.pointerId);
    const move = (ev: PointerEvent) => {
      const w = window.innerWidth;
      const next = Math.min(Math.max(w - ev.clientX, minWidth), w - minRemaining) / w;
      share = next;
    };
    const stop = () => {
      dragging = false;
      target.releasePointerCapture(e.pointerId);
      target.removeEventListener('pointermove', move);
      target.removeEventListener('pointerup', stop);
      target.removeEventListener('pointercancel', stop);
      try {
        localStorage.setItem(widthKey, share.toFixed(3));
      } catch {
        // storage may be unavailable
      }
    };
    target.addEventListener('pointermove', move);
    target.addEventListener('pointerup', stop);
    target.addEventListener('pointercancel', stop);
  }

  function onOpenChange(v: boolean) {
    if (v) return;
    open = false;
    id = '';
  }
</script>

<Dialog.Root open={open || !!id} {onOpenChange}>
  <Dialog.Portal>
    <Dialog.Overlay class="fade fixed inset-0 z-40 bg-black/50" />
    <Dialog.Content
      class="enter-right fixed inset-y-0 right-0 z-50 flex w-full flex-col border-l border-line bg-surface shadow-pop focus:outline-none {dragging ? 'select-none' : ''}"
      style="max-width: {px()}px"
    >
      <div
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize"
        class="group absolute inset-y-0 -left-1 z-10 w-2 cursor-col-resize"
        onpointerdown={startDrag}
      >
        <div class="mx-auto h-full w-px bg-transparent transition-colors group-hover:bg-accent/60 {dragging ? 'bg-accent' : ''}"></div>
      </div>
      <header class="flex items-start gap-3 border-b border-line px-5 py-4">
        <div class="min-w-0 flex-1">
          <Dialog.Title class="truncate text-base font-semibold text-fg">{title}</Dialog.Title>
          {#if subtitle}<Dialog.Description class="mt-0.5 truncate font-mono text-xs text-fg-faint">{subtitle}</Dialog.Description>{/if}
          {#if header}<div class="mt-2">{@render header()}</div>{/if}
        </div>
        <Dialog.Close class="-mr-1 rounded-md p-1 text-fg-faint transition-colors hover:bg-raised hover:text-fg" aria-label="Close">
          <X size={16} />
        </Dialog.Close>
      </header>
      <div class="min-h-0 flex-1 overflow-y-auto">
        {@render children()}
      </div>
      {#if footer}
        <footer class="flex items-center gap-2 border-t border-line bg-bg/40 px-5 py-3">
          {@render footer()}
        </footer>
      {/if}
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
