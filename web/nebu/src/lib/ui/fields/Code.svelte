<script module lang="ts">
	const transit = ['introstart', 'outrostart', 'introend', 'outroend'];
	// Elements mid transition, kept from the events Svelte sends as one starts and ends
	const moving = new WeakSet<EventTarget>();
	for (const type of transit) document.addEventListener(type, (e) => e.target && (type.endsWith('start') ? moving.add(e.target) : moving.delete(e.target)), true);
</script>

<script lang="ts">
	import { onMount } from 'svelte';
	import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
	import { json } from '@codemirror/lang-json';
	import { yaml } from '@codemirror/lang-yaml';
	import { HighlightStyle, syntaxHighlighting, syntaxTree } from '@codemirror/language';
	import { Compartment, EditorState } from '@codemirror/state';
	import { EditorView, keymap, lineNumbers } from '@codemirror/view';
	import { tags } from '@lezer/highlight';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import StateWord from '#lib/ui/StateWord.svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';

	let {
		value = $bindable(''),
		language = 'json',
		label,
		info,
		readonly = false,
		rows = 10,
		fill = false,
		capped = false,
		valid = $bindable(true),
		error = $bindable<string | undefined>(undefined),
		copy = true
	}: { value?: string; language?: 'json' | 'yaml'; label?: string; info?: string; readonly?: boolean; rows?: number; fill?: boolean; capped?: boolean; valid?: boolean; error?: string | undefined; copy?: boolean } = $props();

	let host = $state<HTMLDivElement>();
	let view = $state.raw<EditorView>();
	let stage: HTMLDivElement | undefined;
	const editable = new Compartment();
	const lang = new Compartment();

	const theme = EditorView.theme(
		{
			'&': { color: 'var(--nebu-screen-text)', backgroundColor: 'transparent', fontSize: '13px' },
			'.cm-content': { fontFamily: 'var(--nebu-font-mono)', caretColor: 'var(--nebu-screen-text)', padding: '8px 0' },
			'.cm-gutters': { backgroundColor: 'transparent', color: 'var(--nebu-screen-text-muted)', border: 'none' },
			'.cm-activeLine': { backgroundColor: 'rgb(255 255 255 / 4%)' },
			'.cm-activeLineGutter': { backgroundColor: 'transparent' },
			'&.cm-focused': { outline: 'none' },
			'.cm-selectionBackground, &.cm-focused .cm-selectionBackground': { backgroundColor: 'rgb(159 211 154 / 20%)' },
			'.cm-cursor': { borderLeftColor: 'var(--nebu-screen-text)' }
		},
		{ dark: true }
	);
	const highlight = HighlightStyle.define([
		{ tag: [tags.propertyName, tags.keyword, tags.definition(tags.name)], color: 'var(--nebu-screen-text)', fontWeight: '500' },
		{ tag: [tags.string, tags.number, tags.bool, tags.null], color: 'var(--nebu-screen-text)' },
		{ tag: [tags.comment, tags.punctuation, tags.separator], color: 'var(--nebu-screen-text-muted)' },
		{ tag: tags.invalid, color: 'var(--neo-red-light)' }
	]);

	// Checks the text in its language, JSON by parsing and YAML by its syntax tree
	function check(state: EditorState): string | undefined {
		const text = state.doc.toString();
		if (language === 'json') {
			if (!text.trim()) return undefined;
			try {
				JSON.parse(text);
				return undefined;
			} catch (err) {
				return err instanceof Error ? err.message : String(err);
			}
		}
		let problem: string | undefined;
		syntaxTree(state).iterate({
			enter(node) {
				if (!problem && node.type.isError) problem = `Syntax error at line ${state.doc.lineAt(node.from).number}`;
			}
		});
		return problem;
	}

	// Whether an ancestor is mid transition, its transform skewing every size read beneath it
	function airborne(): boolean {
		for (let el: Element | null | undefined = host; el; el = el.parentElement) if (moving.has(el)) return true;
		return false;
	}

	// Stands an inert copy of the staged editor in the page
	function mirror() {
		if (!stage || !host || !view) return;
		const copy = view.dom.cloneNode(true) as HTMLElement;
		copy.inert = true;
		host.replaceChildren(copy);
	}
	const mirrored = { key: mirror, read: () => null, write: mirror };

	// Holds the editor in a hidden untransformed box of the host's size while an ancestor moves, then returns it
	function settle() {
		if (!host || !view || airborne() === (stage !== undefined)) return;
		if (stage) {
			host.replaceChildren(view.dom);
			stage.remove();
			stage = undefined;
			view.requestMeasure();
			return;
		}
		stage = document.createElement('div');
		stage.className = host.className;
		stage.style.cssText = `${host.style.cssText};position:fixed;top:0;left:0;visibility:hidden;pointer-events:none;width:${host.offsetWidth}px;height:${host.offsetHeight}px;min-height:0;max-height:none`;
		document.body.append(stage);
		mirror();
		stage.append(view.dom);
		view.requestMeasure(mirrored);
	}

	onMount(() => {
		const made = new EditorView({
			parent: host,
			state: EditorState.create({
				doc: value,
				extensions: [
					lineNumbers(),
					history(),
					keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
					lang.of(language === 'json' ? json() : yaml()),
					syntaxHighlighting(highlight),
					theme,
					EditorView.lineWrapping,
					editable.of([EditorView.editable.of(!readonly), EditorState.readOnly.of(readonly)]),
					EditorView.updateListener.of((u) => {
						if (stage) u.view.requestMeasure(mirrored);
						if (!u.docChanged) return;
						value = u.state.doc.toString();
						error = check(u.state);
						valid = error === undefined;
					})
				]
			})
		});
		view = made;
		error = check(made.state);
		valid = error === undefined;
		settle();
		for (const type of transit) document.addEventListener(type, settle, true);
		return () => {
			for (const type of transit) document.removeEventListener(type, settle, true);
			stage?.remove();
			made.destroy();
		};
	});

	$effect(() => {
		if (!view || view.state.doc.toString() === value) return;
		view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value } });
	});

	$effect(() => {
		view?.dispatch({ effects: [editable.reconfigure([EditorView.editable.of(!readonly), EditorState.readOnly.of(readonly)]), lang.reconfigure(language === 'json' ? json() : yaml())] });
	});
</script>

<div class="code" class:filling={fill}>
	<div class="head">
		<FieldLabel {label} {info} />
		<span class="fill"></span>
		{#if capped}<StateWord label="capped" lamp="warning" />{/if}
		{#if copy}<CopyButton text={value} />{/if}
	</div>
	<div class="screen editor" style:--rows="{rows * 1.5 + 1}em" bind:this={host}></div>
	{#if error}<span class="error-text small">{error}</span>{/if}
</div>

<style>
	.code {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.head {
		display: flex;
		gap: 8px;
		align-items: center;
	}
	.editor {
		display: flex;
		flex-direction: column;
		min-height: var(--rows);
		max-height: max(60vh, var(--rows));
		padding: 0 4px;
		overflow: hidden;
	}
	/* Asks its column for a whole viewport and shrinks to the height the column has left */
	.filling {
		flex: 1 1 auto;
		min-height: 0;
	}
	.filling .editor {
		height: 100dvh;
		max-height: none;
	}
	.editor :global(.cm-editor) {
		flex: 1 1 auto;
		min-height: 0;
	}
	.editor :global(.cm-scroller) {
		overflow: auto;
	}
	/* The text area spans the box over a short document, so a press under the last line lands in it */
	.editor :global(.cm-content) {
		min-height: max(100%, calc(var(--rows) - 2px));
	}
	.small {
		font-size: 13px;
	}
</style>
