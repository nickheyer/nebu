<script lang="ts">
  import { Check, Copy } from '@lucide/svelte';

  let { text, label = 'Copy', size = 14 }: { text: string; label?: string; size?: number } = $props();
  let done = $state(false);

  // Copies through the clipboard API, or a hidden textarea outside a secure context
  async function copy() {
    let ok = false;
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
        ok = true;
      } else {
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.setAttribute('readonly', '');
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        ok = document.execCommand('copy');
        document.body.removeChild(ta);
      }
    } catch {
      ok = false;
    }
    if (!ok) return;
    done = true;
    setTimeout(() => (done = false), 1200);
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
