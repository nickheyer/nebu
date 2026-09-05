<script lang="ts">
  import { Dialog } from 'bits-ui';
  import { X } from '@lucide/svelte';
  import type { Snippet } from 'svelte';

  let {
    open = $bindable(false),
    title,
    description,
    size = 'md',
    children,
    footer
  }: {
    open?: boolean;
    title: string;
    description?: string;
    size?: 'sm' | 'md' | 'lg' | 'xl';
    children: Snippet;
    footer?: Snippet;
  } = $props();

  const widths = { sm: 'max-w-md', md: 'max-w-xl', lg: 'max-w-3xl', xl: 'max-w-5xl' };
</script>

<Dialog.Root bind:open>
  <Dialog.Portal>
    <Dialog.Overlay class="fade fixed inset-0 z-40 bg-black/60 backdrop-blur-[2px]" />
    <Dialog.Content
      class="enter-up fixed top-1/2 left-1/2 z-50 flex max-h-[90vh] w-[calc(100vw-2rem)] {widths[size]} -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-xl border border-line bg-overlay shadow-pop focus:outline-none"
    >
      <header class="flex items-start gap-3 px-6 pt-5 pb-4">
        <div class="min-w-0 flex-1">
          <Dialog.Title class="text-lg font-semibold text-fg">{title}</Dialog.Title>
          {#if description}<Dialog.Description class="mt-0.5 truncate text-sm text-fg-muted">{description}</Dialog.Description>{/if}
        </div>
        <Dialog.Close class="-mt-1 -mr-2 rounded-lg p-1.5 text-fg-faint transition-colors hover:bg-raised hover:text-fg" aria-label="Close">
          <X size={16} />
        </Dialog.Close>
      </header>
      <div class="min-h-0 flex-1 overflow-y-auto px-6 pb-5">
        {@render children()}
      </div>
      {#if footer}
        <footer class="flex items-center justify-end gap-2 border-t border-line bg-surface/60 px-6 py-3.5">
          {@render footer()}
        </footer>
      {/if}
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
