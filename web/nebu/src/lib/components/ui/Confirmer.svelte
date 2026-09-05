<script lang="ts">
  import { AlertDialog } from 'bits-ui';
  import { pending, answer } from '$lib/confirm.svelte';
  import Button from './Button.svelte';

  const open = $derived(pending.current !== null);
  const req = $derived(pending.current);
</script>

<AlertDialog.Root
  {open}
  onOpenChange={(v) => {
    if (!v) answer(false);
  }}
>
  <AlertDialog.Portal>
    <AlertDialog.Overlay class="fade fixed inset-0 z-[60] bg-black/60 backdrop-blur-[2px]" />
    <AlertDialog.Content class="enter-up fixed top-1/2 left-1/2 z-[70] w-[calc(100vw-2rem)] max-w-sm -translate-x-1/2 -translate-y-1/2 rounded-xl border border-line bg-overlay p-5 shadow-pop focus:outline-none">
      <AlertDialog.Title class="text-base font-semibold text-fg">{req?.title}</AlertDialog.Title>
      {#if req?.message}
        <AlertDialog.Description class="mt-1.5 text-sm leading-6 text-fg-muted">{req.message}</AlertDialog.Description>
      {/if}
      <div class="mt-5 flex justify-end gap-2">
        <Button variant="ghost" onclick={() => answer(false)}>Cancel</Button>
        <Button variant={req?.tone === 'bad' ? 'danger' : 'primary'} onclick={() => answer(true)}>{req?.action ?? 'Confirm'}</Button>
      </div>
    </AlertDialog.Content>
  </AlertDialog.Portal>
</AlertDialog.Root>
