<script lang="ts">
	import 'katex/dist/katex.min.css';
	import { renderMarkdown, type Resolve } from '#lib/ui/markdown.js';

	let { source, html, resolve }: { source?: string; html?: string; resolve?: Resolve } = $props();

	const rendered = $derived(renderMarkdown({ markdown: source, html }, resolve));
</script>

<div class="markdown">{@html rendered}</div>

<style>
	.markdown {
		min-width: 0;
		line-height: 1.6;
		overflow-wrap: anywhere;
	}
	.markdown :global(h1),
	.markdown :global(h2),
	.markdown :global(h3),
	.markdown :global(h4),
	.markdown :global(h5),
	.markdown :global(h6) {
		margin: 24px 0 8px;
		font-weight: 600;
		line-height: 1.3;
	}
	.markdown :global(h1) {
		font-size: 24px;
	}
	.markdown :global(h2) {
		font-size: 17px;
	}
	.markdown :global(h3),
	.markdown :global(h4),
	.markdown :global(h5),
	.markdown :global(h6) {
		font-size: 15px;
	}
	.markdown :global(h1:first-child),
	.markdown :global(h2:first-child),
	.markdown :global(h3:first-child) {
		margin-top: 0;
	}
	.markdown :global(p),
	.markdown :global(ul),
	.markdown :global(ol),
	.markdown :global(blockquote),
	.markdown :global(pre),
	.markdown :global(.table),
	.markdown :global(details) {
		margin: 0 0 12px;
	}
	.markdown :global(ul),
	.markdown :global(ol) {
		padding-left: 24px;
	}
	.markdown :global(li) {
		margin: 2px 0;
	}
	.markdown :global(a) {
		color: var(--neo-color-primary);
	}
	.markdown :global(pre) {
		padding: 12px;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		font-family: var(--nebu-font-mono);
		font-size: 13px;
		line-height: 1.5;
		color: var(--nebu-screen-text);
		background: var(--nebu-screen-background);
		border: 1px solid var(--nebu-screen-border);
		border-radius: 8px;
		box-shadow: inset 1px 1px 3px rgb(0 0 0 / 50%);
	}
	.markdown :global(pre code) {
		padding: 0;
		font-size: inherit;
		background: none;
	}
	.markdown :global(code) {
		padding: 1px 4px;
		font-family: var(--nebu-font-mono);
		font-size: 13px;
		background: var(--neo-background-color-secondary);
		border-radius: 4px;
	}
	.markdown :global(.table) {
		max-width: 100%;
		overflow: auto;
	}
	.markdown :global(table) {
		border-collapse: collapse;
		font-size: 13px;
	}
	.markdown :global(td),
	.markdown :global(th) {
		padding: 4px 8px;
		text-align: left;
		vertical-align: top;
		border: 1px solid var(--nebu-border);
	}
	.markdown :global(th) {
		font-weight: 500;
	}
	.markdown :global(img),
	.markdown :global(video) {
		max-width: 100%;
		height: auto;
		border-radius: 8px;
	}
	.markdown :global(blockquote) {
		padding: 0 12px;
		color: var(--nebu-text-muted);
		border-left: 2px solid var(--nebu-steel);
	}
	.markdown :global(hr) {
		margin: 16px 0;
		border: 0;
		border-top: 1px solid var(--nebu-border);
	}
	.markdown :global(summary) {
		font-weight: 500;
		cursor: pointer;
	}
	.markdown :global(kbd) {
		padding: 1px 4px;
		font-family: var(--nebu-font-mono);
		font-size: 12px;
		border: 1px solid var(--nebu-border);
		border-radius: 4px;
	}
</style>
