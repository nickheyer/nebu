<script lang="ts">
  import { renderMarkdown, sanitizeHtml } from '$lib/markdown';

  let { markdown = '', html = '', class: cls = '' }: { markdown?: string; html?: string; class?: string } = $props();
  const rendered = $derived(markdown ? renderMarkdown(markdown) : html ? sanitizeHtml(html) : '');
</script>

<div class="prose-nebu {cls}">{@html rendered}</div>
