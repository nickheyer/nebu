<script module lang="ts">
  import { marked } from 'marked';
  import DOMPurify from 'dompurify';

  marked.setOptions({ gfm: true, breaks: false });

  const allowed = {
    ADD_ATTR: ['target', 'rel'],
    FORBID_TAGS: ['style', 'script', 'iframe', 'form', 'input', 'button'],
    USE_PROFILES: { html: true }
  };

  // Prevent new tabs from accessing the opener.
  function harden(html: string): string {
    const doc = new DOMParser().parseFromString(html, 'text/html');
    for (const a of doc.querySelectorAll('a[href]')) {
      a.setAttribute('target', '_blank');
      a.setAttribute('rel', 'noopener noreferrer');
    }
    for (const img of doc.querySelectorAll('img')) img.setAttribute('loading', 'lazy');
    for (const media of doc.querySelectorAll('video, audio')) {
      media.setAttribute('controls', '');
      media.setAttribute('preload', 'metadata');
    }
    for (const video of doc.querySelectorAll('video')) video.setAttribute('playsinline', '');
    return doc.body.innerHTML;
  }

  // Strip YAML front matter before rendering and sanitizing Markdown. With
  // breaks, a single newline becomes a line break, as in typed dialogue.
  function renderMarkdown(md: string, breaks: boolean): string {
    const body = md.replace(/^---\r?\n[\s\S]*?\r?\n---\r?\n?/, '');
    return harden(DOMPurify.sanitize(marked.parse(body, { async: false, breaks }) as string, allowed));
  }
</script>

<script lang="ts">
  let { markdown = '', html = '', breaks = false }: { markdown?: string; html?: string; breaks?: boolean } = $props();
  const rendered = $derived(markdown ? renderMarkdown(markdown, breaks) : html ? harden(DOMPurify.sanitize(html, allowed)) : '');
</script>

<div class="prose-nebu">{@html rendered}</div>
