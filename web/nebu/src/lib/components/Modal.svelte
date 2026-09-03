<script lang="ts">
  import { Dialog } from 'bits-ui';
  import type { Snippet } from 'svelte';
  let { open = $bindable(false), title, children }: { open?: boolean; title: string; children: Snippet } = $props();
</script>

<Dialog.Root bind:open>
  <Dialog.Portal>
    <Dialog.Overlay class="fixed inset-0 z-40 bg-black/70" />
    <Dialog.Content class="fixed top-1/2 left-1/2 z-50 w-[min(40rem,95vw)] -translate-x-1/2 -translate-y-1/2 rounded-lg border border-zinc-700 bg-zinc-900 p-5 shadow-xl">
      <div class="mb-3 flex items-center justify-between">
        <Dialog.Title class="h2">{title}</Dialog.Title>
        <Dialog.Close class="btn">close</Dialog.Close>
      </div>
      {@render children()}
    </Dialog.Content>
  </Dialog.Portal>
</Dialog.Root>
