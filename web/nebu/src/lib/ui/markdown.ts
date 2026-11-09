import DOMPurify from 'dompurify';
import katex from 'katex';
import { Marked, type Tokens } from 'marked';

// Where relative paths of a document resolve, as a download and as a page
export type Resolve = { asset: (path: string) => string; link: (path: string) => string };

const sanitize = {
	USE_PROFILES: { html: true, mathMl: true },
	ADD_TAGS: ['video', 'source', 'audio', 'details', 'summary', 'picture', 'figure', 'figcaption', 'kbd'],
	ADD_ATTR: ['target', 'align', 'width', 'height', 'controls', 'autoplay', 'loop', 'muted', 'playsinline', 'poster', 'preload', 'open', 'start'],
	FORBID_TAGS: ['style', 'form', 'input', 'button', 'select', 'textarea'],
	// Foreign colours and inline layout drop so a card reads in the app's own palette
	FORBID_ATTR: ['style', 'bgcolor', 'color', 'background']
};

function escape(s: string): string {
	return s.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;');
}

// Drops YAML front matter
export function stripFrontMatter(markdown: string): string {
	const m = /^﻿?---[ \t]*\r?\n[\s\S]*?\r?\n---[ \t]*(?:\r?\n|$)/.exec(markdown);
	return m ? markdown.slice(m[0].length) : markdown;
}

function math(tex: string, displayMode: boolean): string {
	return katex.renderToString(tex, { displayMode, throwOnError: false, strict: 'ignore' });
}

const blockMath = {
	name: 'blockMath',
	level: 'block' as const,
	start(src: string) {
		const i = src.search(/\$\$|\\\[/);
		return i < 0 ? undefined : i;
	},
	tokenizer(src: string) {
		const m = /^\$\$([\s\S]+?)\$\$(?:\n+|$)/.exec(src) ?? /^\\\[([\s\S]+?)\\\](?:\n+|$)/.exec(src);
		if (!m) return undefined;
		return { type: 'blockMath', raw: m[0], text: (m[1] ?? '').trim() };
	},
	renderer(token: Tokens.Generic) {
		return `<p>${math(String(token.text), true)}</p>\n`;
	}
};

const inlineMath = {
	name: 'inlineMath',
	level: 'inline' as const,
	start(src: string) {
		const i = src.search(/\$(?!\s)|\\\(/);
		return i < 0 ? undefined : i;
	},
	tokenizer(src: string) {
		const m = /^\$(?!\s)((?:\\.|[^\\$\n])+?)(?<!\s)\$(?!\d)/.exec(src) ?? /^\\\(((?:\\.|[^\\\n])+?)\\\)/.exec(src);
		if (!m) return undefined;
		return { type: 'inlineMath', raw: m[0], text: m[1] ?? '' };
	},
	renderer(token: Tokens.Generic) {
		return math(String(token.text), false);
	}
};

const marked = new Marked({
	gfm: true,
	breaks: false,
	extensions: [blockMath, inlineMath],
	renderer: {
		code({ text, lang }) {
			const name = (lang ?? '').trim().split(/\s+/)[0] ?? '';
			const cls = name ? ` class="language-${escape(name)}"` : '';
			return `<pre><code${cls}>${escape(text)}\n</code></pre>\n`;
		}
	}
});

function isRelative(url: string): boolean {
	return !/^(?:[a-z][a-z0-9+.-]*:|\/\/|#)/i.test(url);
}

// Resolves relative files, opens outside links in a new tab and boxes tables
function groom(root: HTMLElement, resolve?: Resolve): void {
	for (const el of root.querySelectorAll<HTMLElement>('img[src], video[src], source[src], audio[src], video[poster]')) {
		for (const attr of ['src', 'poster']) {
			const v = el.getAttribute(attr);
			if (v && resolve && isRelative(v)) el.setAttribute(attr, resolve.asset(v));
		}
		if (el.tagName === 'IMG') el.setAttribute('loading', 'lazy');
	}
	for (const a of root.querySelectorAll<HTMLAnchorElement>('a[href]')) {
		const href = a.getAttribute('href') ?? '';
		if (href.startsWith('#')) continue;
		if (resolve && isRelative(href)) a.setAttribute('href', resolve.link(href));
		a.setAttribute('target', '_blank');
		a.setAttribute('rel', 'nofollow noreferrer');
	}
	for (const table of root.querySelectorAll('table')) {
		const box = root.ownerDocument.createElement('div');
		box.className = 'table';
		table.replaceWith(box);
		box.append(table);
	}
}

// Renders sanitized html from markdown or from html the source rendered
export function renderMarkdown(doc: { markdown?: string; html?: string }, resolve?: Resolve): string {
	const html = doc.html || (marked.parse(stripFrontMatter(doc.markdown ?? ''), { async: false }) as string);
	const root = DOMPurify.sanitize(html, { ...sanitize, RETURN_DOM: true }) as HTMLElement;
	groom(root, resolve);
	return root.innerHTML;
}
