<script lang="ts">
  import { Check, Copy } from '@lucide/svelte';
  import Tip from './Tip.svelte';

  let { text, label = 'Copy', size = 14, class: cls = '' }: { text: string; label?: string; size?: number; class?: string } = $props();
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

<Tip text={done ? 'Copied' : label}>
  <button
    type="button"
    class="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-fg-faint transition-colors hover:bg-raised hover:text-fg {done ? 'text-ok hover:text-ok' : ''} {cls}"
    onclick={(e) => {
      e.stopPropagation();
      copy();
    }}
    aria-label={label}
  >
    {#if done}<Check {size} />{:else}<Copy {size} />{/if}
  </button>
</Tip>
