<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { isCancel, reason } from '#lib/api/transport.js';
	import { num, plural } from '#lib/format.js';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import DownloadIcon from 'phosphor-svelte/lib/DownloadIcon';

	let {
		source,
		name = 'log',
		levels = false,
		// Whether lines can still arrive, off for the log of something that has ended
		growing = true,
		height = '50vh'
	}: { source: (tail: number, follow: boolean, signal: AbortSignal) => AsyncIterable<string[]>; name?: string; levels?: boolean; growing?: boolean; height?: string } = $props();

	// A line keeps its arrival order and whether it landed after the tail
	type Line = { seq: number; text: string; fresh: boolean };

	let follow = $state(true);
	let tail = $state(1000);
	let filter = $state('');
	let lines = $state<Line[]>([]);
	let seq = 0;
	let error = $state<string | null>(null);
	let pre = $state<HTMLPreElement>();
	let pinned = $state(true);

	const shown = $derived(filter ? lines.filter((l) => l.text.toLowerCase().includes(filter.toLowerCase())) : lines);
	// A log that has ended and came back shorter than the tail asked for is all there is, so a longer tail would show no more
	const whole = $derived(!growing && lines.length < tail);

	$effect(() => {
		const t = tail;
		const f = follow;
		const src = source;
		const abort = new AbortController();
		lines = [];
		error = null;
		let landed = false;
		void (async () => {
			try {
				for await (const batch of src(t, f, abort.signal)) {
					const fresh = landed;
					landed = true;
					if (!batch.length) continue;
					lines = [...lines, ...batch.map((text) => ({ seq: seq++, text, fresh }))].slice(-Math.max(t, 200));
				}
			} catch (err) {
				if (!abort.signal.aborted && !isCancel(err)) error = reason(err);
			}
		})();
		return () => abort.abort();
	});

	$effect(() => {
		if (shown.length && pre && pinned) pre.scrollTop = pre.scrollHeight;
	});

	function scrolled() {
		if (!pre) return;
		pinned = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 24;
	}

	function levelOf(line: string): string {
		if (!levels) return '';
		const head = line.slice(0, 48).toLowerCase();
		if (/\b(error|erro|err|fatal|panic)\b/.test(head)) return 'error';
		if (/\b(warn|warning)\b/.test(head)) return 'warning';
		if (/\b(debug|dbg|trace)\b/.test(head)) return 'dim';
		return '';
	}

	// A line cut after the separators inside its long tokens, where it wraps before a word has to break
	function pieces(text: string): string[] {
		return /\S{24}/.test(text) ? text.split(/(?<=\S{6}[/=,;:&?.])(?=\S{6})/) : [text];
	}

	function download() {
		const blob = new Blob([lines.map((l) => l.text).join('\n')], { type: 'text/plain' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = `${name}.log`;
		a.click();
		URL.revokeObjectURL(url);
	}
</script>

<div class="logview">
	<div class="tools dense">
		{#if whole}
			<span class="count secondary">{plural(lines.length, 'line')}{#if filter}, {num(shown.length)} shown{/if}</span>
		{:else}
			<Select bind:value={tail} options={[{ value: 200, label: 'Last 200 lines' }, { value: 1000, label: 'Last 1000 lines' }, { value: 5000, label: 'Last 5000 lines' }]} fit />
		{/if}
		<span class="filter"><Text bind:value={filter} label="Show lines holding" compact /></span>
		<span class="acts">
			{#if growing}<Switch bind:checked={follow} label="Follow" compact />{/if}
			<CopyButton text={shown.map((l) => l.text).join('\n')} label="Copy the lines shown" />
			<NeoButton text aria-label="Download the log" title="Download the log" onclick={download}>
				{#snippet icon()}<DownloadIcon size={20} />{/snippet}
			</NeoButton>
		</span>
	</div>
	<div class="screen well" style:--well={height}>
		<pre class="log" bind:this={pre} onscroll={scrolled}>{#each shown as line (line.seq)}<span class="line {levelOf(line.text)}" class:fresh={line.fresh} onanimationend={() => (line.fresh = false)}>{#each pieces(line.text) as piece, i (i)}{#if i}<wbr />{/if}{piece}{/each}</span>{/each}</pre>
	</div>
	{#if error}<span class="error-text small">{error}</span>{/if}
</div>

<style>
	.logview {
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
	}
	.tools {
		display: flex;
		flex-wrap: wrap;
		gap: 12px;
		align-items: center;
	}
	.filter {
		display: flex;
		flex: 1 1 12rem;
		min-width: 0;
		max-width: 22rem;
	}
	.filter > :global(*) {
		flex: 1 1 auto;
		min-width: 0;
	}
	/* The switch and the two glyphs keep one line together at the row's end */
	.acts {
		display: inline-flex;
		flex: 0 0 auto;
		gap: var(--nebu-space-2);
		align-items: center;
		margin-left: auto;
	}
	.count {
		white-space: nowrap;
	}
	.well {
		display: flex;
		min-width: 0;
		padding: 12px;
	}
	/* The scrolling text is a whole number of lines tall, so its first and last lines are never cut through */
	.log {
		flex: 1 1 auto;
		min-width: 0;
		height: round(down, calc(var(--well) - 26px), 1lh);
		margin: 0;
		overflow: auto;
		font: inherit;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	/* An entry that wraps hangs its later lines in from its first */
	.line {
		display: block;
		padding-left: 4ch;
		text-indent: -4ch;
	}
	.line.fresh {
		animation: nebu-phosphor 1600ms cubic-bezier(0.1, 0.7, 0.3, 1) both;
	}
	.line.error {
		color: var(--neo-red-light);
	}
	.line.warning {
		color: var(--neo-orange-light);
	}
	.line.dim {
		color: var(--nebu-screen-text-muted);
	}
	.small {
		font-size: 13px;
	}
</style>
