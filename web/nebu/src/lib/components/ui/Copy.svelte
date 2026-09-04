<script lang="ts">
  import { Check, Copy } from '@lucide/svelte';
  import { copyText } from '$lib/clipboard';

  let { text, label = 'Copy', size = 14 }: { text: string; label?: string; size?: number } = $props();
  let done = $state(false);

  async function copy() {
    if (await copyText(text)) {
      done = true;
      setTimeout(() => (done = false), 1200);
    }
  }
</script>

<button
  type="button"
  class="inline-flex shrink-0 items-center justify-center rounded p-1 text-fg-faint transition-colors hover:bg-raised hover:text-fg {done ? 'text-ok' : ''}"
  onclick={copy}
  title={label}
  aria-label={label}
>
  {#if done}<Check {size} />{:else}<Copy {size} />{/if}
</button>
