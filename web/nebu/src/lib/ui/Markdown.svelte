<script lang="ts">
	import 'katex/dist/katex.min.css';
	import { renderMarkdown, type Resolve } from '#lib/ui/markdown.js';

	let { source, html, resolve }: { source?: string; html?: string; resolve?: Resolve } = $props();

	const rendered = $derived(renderMarkdown({ markdown: source, html }, resolve));
</script>

<article class="markdown">{@html rendered}</article>

<style>
	/* A reading measure that never widens its column */
	.markdown {
		width: 100%;
		max-width: 76ch;
		min-width: 0;
		margin: 0 auto;
		font-weight: 300;
		line-height: 1.65;
		overflow-wrap: anywhere;
		contain: inline-size;
	}
	.markdown :global(h1),
	.markdown :global(h2),
	.markdown :global(h3),
	.markdown :global(h4),
	.markdown :global(h5),
	.markdown :global(h6) {
		margin: 28px 0 8px;
		font-weight: 500;
		line-height: 1.3;
	}
	.markdown :global(h1) {
		font-size: 24px;
		font-weight: 400;
	}
	.markdown :global(h2) {
		font-size: 18px;
	}
	.markdown :global(h3),
	.markdown :global(h4),
	.markdown :global(h5),
	.markdown :global(h6) {
		font-size: 15px;
	}
	.markdown :global(:is(h1, h2, h3):first-child) {
		margin-top: 0;
	}
	.markdown :global(p),
	.markdown :global(ul),
	.markdown :global(ol),
	.markdown :global(blockquote),
	.markdown :global(pre),
	.markdown :global(.table),
	.markdown :global(details),
	.markdown :global(figure) {
		margin: 0 0 14px;
	}
	.markdown :global(strong) {
		font-weight: 500;
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
	.markdown :global([align='center']),
	.markdown :global(center) {
		text-align: center;
	}
	.markdown :global([align='center'] img),
	.markdown :global(center img) {
		display: inline-block;
	}
	.markdown :global(pre) {
		padding: 12px;
		overflow-x: auto;
		font-family: var(--nebu-font-mono);
		font-size: 13px;
		font-weight: 400;
		line-height: 1.5;
		color: var(--nebu-screen-text);
		white-space: pre;
		background: var(--nebu-screen-background);
		border: 1px solid var(--nebu-screen-border);
		border-radius: 8px;
		box-shadow: var(--nebu-bezel);
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
		font-weight: 400;
		background: var(--neo-background-color-secondary);
		border-radius: 4px;
	}
	.markdown :global(.table) {
		max-width: 100%;
		overflow-x: auto;
	}
	.markdown :global(table) {
		min-width: max-content;
		margin: 0 auto;
		font-size: 13px;
		border-collapse: collapse;
	}
	.markdown :global(td),
	.markdown :global(th) {
		padding: 4px 10px;
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
		vertical-align: middle;
		border-radius: 6px;
	}
	.markdown :global(p > img:only-child),
	.markdown :global(p > a:only-child > img:only-child) {
		display: block;
		margin: 0 auto;
	}
	.markdown :global(.katex-display) {
		overflow-x: auto;
		overflow-y: hidden;
	}
	.markdown :global(blockquote) {
		padding: 0 12px;
		color: var(--nebu-text-muted);
		border-left: 2px solid var(--nebu-steel);
	}
	.markdown :global(hr) {
		margin: 20px 0;
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
