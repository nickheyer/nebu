<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoDialog } from '#lib/neo/floating/dialog/index.js';
	import { reason } from '#lib/api/transport.js';

	let {
		title,
		stream,
		onclose
	}: {
		title: string;
		stream: ((signal: AbortSignal) => AsyncIterable<string[]>) | undefined;
		onclose: () => void;
	} = $props();

	let lines = $state<string[]>([]);
	let error = $state<string | null>(null);
	let pre = $state<HTMLPreElement | undefined>(undefined);

	$effect(() => {
		const source = stream;
		if (!source) return;
		lines = [];
		error = null;
		const abort = new AbortController();
		void follow(source, abort.signal);
		return () => abort.abort();
	});

	$effect(() => {
		if (lines.length && pre) pre.scrollTop = pre.scrollHeight;
	});

	async function follow(source: (signal: AbortSignal) => AsyncIterable<string[]>, signal: AbortSignal) {
		try {
			for await (const chunk of source(signal)) {
				if (chunk.length) lines = [...lines, ...chunk];
			}
		} catch (err) {
			if (!signal.aborted) error = reason(err);
		}
	}
</script>

<NeoDialog open={stream !== undefined} modal rounded elevation={3} width="min(64rem, 94vw)" padding="1.5rem" {onclose}>
	<h2 class="dialog-title">{title}</h2>
	<pre class="log" bind:this={pre}>{lines.join('\n')}</pre>
	{#if error}<p class="muted">{error}</p>{/if}
	<div class="dialog-actions">
		<NeoButton rounded onclick={onclose}>Close</NeoButton>
	</div>
</NeoDialog>
