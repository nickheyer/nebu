<script lang="ts">
  import { Info } from '@lucide/svelte';
  import Tip from './Tip.svelte';

  // A small mark in a container's corner, its tooltip the one place a short explanation lives
  let { text, class: cls = '' }: { text: string; class?: string } = $props();
</script>

<Tip {text}>
  <span class="inline-flex h-5 w-5 cursor-help items-center justify-center rounded-full text-fg-faint transition-colors hover:bg-raised hover:text-fg {cls}" aria-label={text}>
    <Info size={13} />
  </span>
</Tip>
