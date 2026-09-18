<script lang="ts">
  import { Dialog } from 'bits-ui';
  import { X } from '@lucide/svelte';

  // An image shown at full size over the page, closed by a click anywhere or Escape
  let { src = $bindable(''), alt = '' }: { src?: string; alt?: string } = $props();
</script>

<Dialog.Root
  bind:open={
    () => !!src,
    (v) => {
      if (!v) src = '';
    }
  }
>
  <Dialog.Portal>
    <Dialog.Overlay class="fade fixed inset-0 z-[60] bg-black/80 backdrop-blur-[2px]" />
    <Dialog.Content class="fixed inset-0 z-[70] flex items-center justify-center p-6 focus:outline-none" onclick={() => (src = '')}>
      <Dialog.Title class="sr-only">{alt || 'Image'}</Dialog.Title>
      <Dialog.Description class="sr-only">Click anywhere or press Escape to close</Dialog.Description>
      <img {src} {alt} class="max-h-full max-w-full rounded-md object-contain shadow-pop" />
      <Dialog.Close class="absolute top-4 right-4 rounded-md bg-raised/80 p-2 text-fg-muted transition-colors hover:bg-raised hover:text-fg" aria-label="Close"><X size={16} /></Dialog.Close>
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
