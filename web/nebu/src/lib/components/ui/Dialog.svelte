<script lang="ts">
  import { Dialog } from 'bits-ui';
  import { X } from '@lucide/svelte';
  import type { Snippet } from 'svelte';

  let {
    open = $bindable(false),
    title,
    description,
    size = 'md',
    mono = false,
    children,
    footer,
    onclose
  }: { open?: boolean; title: string; description?: string; size?: 'sm' | 'md' | 'lg' | 'xl'; mono?: boolean; children: Snippet; footer?: Snippet; onclose?: () => void } = $props();

  const widths = { sm: 'max-w-md', md: 'max-w-xl', lg: 'max-w-3xl', xl: 'max-w-5xl' };
</script>

<Dialog.Root
  bind:open
  onOpenChange={(v) => {
    if (!v) onclose?.();
  }}
>
  <Dialog.Portal>
    <Dialog.Overlay class="fade fixed inset-0 z-[60] bg-black/60 backdrop-blur-[2px]" />
    <Dialog.Content class="enter-up fixed top-1/2 left-1/2 z-[70] flex max-h-[calc(100vh-3rem)] w-[calc(100vw-2rem)] -translate-x-1/2 -translate-y-1/2 flex-col rounded-xl border border-line bg-overlay shadow-pop focus:outline-none {widths[size]}">
      <header class="flex items-start gap-3 border-b border-line px-6 py-4">
        <div class="min-w-0 flex-1">
          <Dialog.Title class="truncate text-base font-semibold text-fg {mono ? 'font-mono' : ''}">{title}</Dialog.Title>
          {#if description}<Dialog.Description class="mt-0.5 text-sm text-fg-muted">{description}</Dialog.Description>{/if}
        </div>
        <Dialog.Close class="-mt-1 -mr-2 rounded-md p-1.5 text-fg-faint transition-colors hover:bg-raised hover:text-fg" aria-label="Close"><X size={15} /></Dialog.Close>
      </header>
      <div class="min-h-0 flex-1 overflow-y-auto px-6 py-5">
        {@render children()}
      </div>
      {#if footer}
        <footer class="flex items-center gap-2 border-t border-line bg-sunken/40 px-6 py-3">
          {@render footer()}
        </footer>
      {/if}
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
