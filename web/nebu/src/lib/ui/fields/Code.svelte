<script lang="ts">
	import { onMount } from 'svelte';
	import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
	import { json } from '@codemirror/lang-json';
	import { yaml } from '@codemirror/lang-yaml';
	import { HighlightStyle, syntaxHighlighting, syntaxTree } from '@codemirror/language';
	import { Compartment, EditorState } from '@codemirror/state';
	import { EditorView, keymap, lineNumbers } from '@codemirror/view';
	import { tags } from '@lezer/highlight';
	import { NeoPill } from '#lib/neo/pill/index.js';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';

	let {
		value = $bindable(''),
		language = 'json',
		label,
		readonly = false,
		rows = 10,
		capped = false,
		valid = $bindable(true),
		error = $bindable<string | undefined>(undefined),
		copy = true
	}: { value?: string; language?: 'json' | 'yaml'; label?: string; readonly?: boolean; rows?: number; capped?: boolean; valid?: boolean; error?: string | undefined; copy?: boolean } = $props();

	let host = $state<HTMLDivElement>();
	let view: EditorView | undefined;
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

	onMount(() => {
		view = new EditorView({
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
						if (!u.docChanged) return;
						value = u.state.doc.toString();
						error = check(u.state);
						valid = error === undefined;
					})
				]
			})
		});
		error = check(view.state);
		valid = error === undefined;
		return () => view?.destroy();
	});

	$effect(() => {
		if (!view || view.state.doc.toString() === value) return;
		view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value } });
	});

	$effect(() => {
		view?.dispatch({ effects: [editable.reconfigure([EditorView.editable.of(!readonly), EditorState.readOnly.of(readonly)]), lang.reconfigure(language === 'json' ? json() : yaml())] });
	});
</script>

<div class="code">
	<div class="head">
		<FieldLabel {label} />
		<span class="fill"></span>
		{#if capped}<NeoPill size="small" text tinted color="warning" label="capped" />{/if}
		{#if copy}<CopyButton text={value} />{/if}
	</div>
	<div class="screen editor" style:min-height="{rows * 1.5 + 1}em" bind:this={host}></div>
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
		padding: 0 4px;
		overflow: hidden;
	}
	.editor :global(.cm-editor) {
		max-height: 60vh;
	}
	.editor :global(.cm-scroller) {
		overflow: auto;
	}
	.small {
		font-size: 13px;
	}
</style>
