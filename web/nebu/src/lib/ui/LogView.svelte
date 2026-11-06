<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { isCancel, reason } from '#lib/api/transport.js';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import DownloadIcon from 'phosphor-svelte/lib/DownloadIcon';

	let {
		// Opens a stream of line batches for the tail and whether to follow
		source,
		name = 'log',
		// Whether lines start with a level word to colour
		levels = false,
		height = '50vh'
	}: { source: (tail: number, follow: boolean, signal: AbortSignal) => AsyncIterable<string[]>; name?: string; levels?: boolean; height?: string } = $props();

	let follow = $state(true);
	let tail = $state(1000);
	let filter = $state('');
	let lines = $state<string[]>([]);
	let error = $state<string | null>(null);
	let pre = $state<HTMLPreElement>();
	let pinned = $state(true);

	const shown = $derived(filter ? lines.filter((l) => l.toLowerCase().includes(filter.toLowerCase())) : lines);

	$effect(() => {
		const t = tail;
		const f = follow;
		const src = source;
		const abort = new AbortController();
		lines = [];
		error = null;
		void (async () => {
			try {
				for await (const batch of src(t, f, abort.signal)) {
					if (batch.length) lines = [...lines, ...batch].slice(-Math.max(t, 200));
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

	function download() {
		const blob = new Blob([lines.join('\n')], { type: 'text/plain' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = `${name}.log`;
		a.click();
		URL.revokeObjectURL(url);
	}
</script>

<div class="logview">
	<div class="tools">
		<Switch bind:checked={follow} label="Follow" />
		<Select bind:value={tail} options={[{ value: 200, label: '200 lines' }, { value: 1000, label: '1000 lines' }, { value: 5000, label: '5000 lines' }]} width="12rem" />
		<Text bind:value={filter} placeholder="Filter" />
		<span class="fill"></span>
		<CopyButton text={shown.join('\n')} />
		<NeoButton text aria-label="Download" onclick={download}>
			{#snippet icon()}<DownloadIcon size={20} />{/snippet}
		</NeoButton>
	</div>
	<pre class="screen log" style:height bind:this={pre} onscroll={scrolled}>{#each shown as line, i (i)}<span class="line {levelOf(line)}">{line}
</span>{/each}</pre>
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
	.log {
		margin: 0;
		padding: 12px;
		overflow: auto;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
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
