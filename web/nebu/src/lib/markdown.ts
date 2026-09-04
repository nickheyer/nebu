import { marked } from 'marked';
import DOMPurify from 'dompurify';

marked.setOptions({ gfm: true, breaks: false });

const allowed = {
  ADD_ATTR: ['target', 'rel'],
  FORBID_TAGS: ['style', 'script', 'iframe', 'form', 'input', 'button'],
  USE_PROFILES: { html: true }
};

// Opens links in a new tab without leaking the opener
function harden(html: string): string {
  const doc = new DOMParser().parseFromString(html, 'text/html');
  for (const a of doc.querySelectorAll('a[href]')) {
    a.setAttribute('target', '_blank');
    a.setAttribute('rel', 'noopener noreferrer');
  }
  for (const img of doc.querySelectorAll('img')) img.setAttribute('loading', 'lazy');
  return doc.body.innerHTML;
}

// Strips a YAML front matter block a model card starts with
export function stripFrontMatter(md: string): { body: string; meta: string } {
  const m = md.match(/^---\r?\n([\s\S]*?)\r?\n---\r?\n?/);
  if (!m) return { body: md, meta: '' };
  return { body: md.slice(m[0].length), meta: m[1] };
}

// Renders markdown to sanitized HTML safe to inject
export function renderMarkdown(md: string): string {
  const { body } = stripFrontMatter(md);
  const raw = marked.parse(body, { async: false }) as string;
  return harden(DOMPurify.sanitize(raw, allowed));
}

// Sanitizes HTML a source published, such as a description
export function sanitizeHtml(html: string): string {
  return harden(DOMPurify.sanitize(html, allowed));
}
