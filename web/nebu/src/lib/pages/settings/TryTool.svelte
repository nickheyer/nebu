<script lang="ts">
	import { reason, rpc } from '#lib/api/transport.js';
	import { ms, plural } from '#lib/format.js';
	import type { ToolInfo, ToolPart, ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Mark from '#lib/ui/Mark.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import Code from '#lib/ui/fields/Code.svelte';
	import Select from '#lib/ui/fields/Select.svelte';

	// Calls one of a server's tools with arguments seeded from its schema and shows what it answered
	let { open = $bindable(false), server }: { open?: boolean; server: ToolServerStatus } = $props();

	let tool = $state<string | undefined>(undefined);
	let args = $state('{}');
	let valid = $state(true);
	let seedError = $state('');
	let busy = $state(false);
	let error = $state('');
	let result = $state<{ parts: ToolPart[]; failed: boolean; tookMs: number } | undefined>(undefined);

	$effect(() => {
		if (!open) return;
		tool = server.tools[0]?.name;
		result = undefined;
	});

	$effect(() => {
		const seeded = seed(server.tools.find((x) => x.name === tool));
		args = seeded.text;
		seedError = seeded.error;
		result = undefined;
		error = '';
	});

	// A JSON object with every schema property set to its default or an empty value of its type
	function seed(t: ToolInfo | undefined): { text: string; error: string } {
		if (!t || !t.schema.trim()) return { text: '{}', error: '' };
		let schema: { properties?: Record<string, { type?: string; default?: unknown }> };
		try {
			schema = JSON.parse(t.schema) as typeof schema;
		} catch (err) {
			return { text: '{}', error: `Schema unreadable ${reason(err)}` };
		}
		const out: Record<string, unknown> = {};
		for (const [k, v] of Object.entries(schema.properties ?? {})) {
			out[k] = v.default ?? (v.type === 'number' || v.type === 'integer' ? 0 : v.type === 'boolean' ? false : v.type === 'array' ? [] : v.type === 'object' ? {} : '');
		}
		return { text: JSON.stringify(out, null, 2), error: '' };
	}

	async function run() {
		if (!tool) return;
		busy = true;
		error = '';
		try {
			const res = await rpc.tools.callTool({ name: tool, arguments: args, nodeId: server.nodeId });
			result = { parts: res.parts, failed: res.failed, tookMs: res.tookMs };
		} catch (err) {
			error = reason(err);
		} finally {
			busy = false;
		}
	}
</script>

<Dialog bind:open title="Try {server.server?.name ?? ''}" confirm="Run" cancel="Close" {busy} disabled={!tool || !valid} onconfirm={run} width="full">
	<Select bind:value={tool} options={server.tools.map((t) => ({ value: t.name, label: t.name, description: t.description }))} label="Tool" info="One tool the server offers" placeholder="Choose a tool" />
	<Code bind:value={args} bind:valid language="json" rows={6} label="Arguments" info="The call's arguments as JSON" />
	{#if result}
		<div class="result">
			<Mark state={result.failed ? 'failed' : 'ok'} label={result.failed ? 'Failed' : 'Succeeded'} />
			<span class="secondary">{ms(result.tookMs)}</span>
			<span class="secondary">{plural(result.parts.length, 'part')}</span>
		</div>
		{#each result.parts as p, i (i)}
			{#if p.type === 'image'}
				<img class="partimg" src="data:{p.mediaType};base64,{p.data}" alt="Part {i + 1}" />
			{:else}
				<Screen maxHeight="40vh"><div class="part">{p.text}</div></Screen>
			{/if}
		{/each}
	{/if}
	{#snippet footer()}
		{#if error || seedError}<span class="error-text">{error || seedError}</span>{/if}
	{/snippet}
</Dialog>

<style>
	.result {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
	}
	.part {
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.partimg {
		max-width: 100%;
		border-radius: var(--neo-border-radius);
	}
</style>
