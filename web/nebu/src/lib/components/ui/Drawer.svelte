<script lang="ts">
  import { Dialog } from 'bits-ui';
  import { X } from '@lucide/svelte';
  import type { Snippet } from 'svelte';

  let {
    open = $bindable(false),
    id = $bindable(''),
    title,
    subtitle,
    width = 'md',
    header,
    children,
    footer
  }: {
    open?: boolean;
    // Bound instead of open by drawers showing one record, empty when closed
    id?: string;
    title: string;
    subtitle?: string;
    width?: 'md' | 'lg' | 'xl' | '2xl';
    header?: Snippet;
    children: Snippet;
    footer?: Snippet;
  } = $props();

  const widths = { md: 'max-w-xl', lg: 'max-w-2xl', xl: 'max-w-4xl', '2xl': 'max-w-6xl' };

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
      class="enter-right fixed inset-y-0 right-0 z-50 flex w-full {widths[width]} flex-col border-l border-line bg-surface shadow-pop focus:outline-none"
    >
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
