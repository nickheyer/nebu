<script lang="ts">
	import { untrack } from 'svelte';
	import { shortId } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { ChatSettings } from '#lib/proto/nebu/v1/chat_pb.js';
	import { ToolServerState, type ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Slider from '#lib/ui/fields/Slider.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Textarea from '#lib/ui/fields/Textarea.svelte';
	import type { Option } from '#lib/ui/fields/types.js';

	// The request settings of one conversation, an empty sampling field leaving that setting to the runtime
	let {
		settings = $bindable(),
		servers,
		contextLength,
		userFallback,
		assistantFallback
	}: { settings: ChatSettings; servers: ToolServerStatus[]; contextLength: number; userFallback: string; assistantFallback: string } = $props();

	type NumKey = 'temperature' | 'topP' | 'topK' | 'maxTokens' | 'seed';

	const apis = [
		{ value: 'openai', label: 'OpenAI' },
		{ value: 'anthropic', label: 'Anthropic' },
		{ value: 'ollama', label: 'Ollama' }
	];

	function read(key: NumKey): number | undefined {
		const v = settings[key] === '' ? NaN : globalThis.Number(settings[key]);
		return globalThis.Number.isNaN(v) ? undefined : v;
	}

	function write(key: NumKey, n: number | undefined) {
		settings[key] = n === undefined ? '' : String(n);
	}

	let userName = $state(untrack(() => settings.userName || userFallback));
	let assistantName = $state(untrack(() => settings.assistantName || assistantFallback));

	// Each name box holds the name in use, its own or the one it resolves to
	$effect(() => {
		userName = settings.userName || userFallback;
		assistantName = settings.assistantName || assistantFallback;
	});

	// Keeps a name typed over the resolved one, an emptied box returning to the resolved name
	function rename() {
		settings.userName = userName.trim() === userFallback ? '' : userName.trim();
		settings.assistantName = assistantName.trim() === assistantFallback ? '' : assistantName.trim();
		userName = settings.userName || userFallback;
		assistantName = settings.assistantName || assistantFallback;
	}

	// Every tool server on offer, one the conversation names and nobody runs kept so it can be unchecked
	const serverOptions = $derived.by((): Option<string>[] => {
		const options = servers.map((s): Option<string> => {
			const value = s.ref || s.server?.name || '';
			const node = s.nodeId ? live.nodes.get(s.nodeId)?.name || shortId(s.nodeId) : undefined;
			const failed = s.state === ToolServerState.FAILED;
			return { value, label: s.server?.name || value, description: node, disabled: failed && !settings.toolServers.includes(value), tip: failed ? s.error : undefined };
		});
		for (const ref of settings.toolServers) if (!options.some((o) => o.value === ref)) options.push({ value: ref, label: ref, description: 'missing' });
		return options;
	});
</script>

<div class="sections dense">
	<div class="fields">
		<Textarea bind:value={settings.system} label="System prompt" rows={3} maxRows={12} />
		<div class="names">
			<Text bind:value={userName} label="User" max={32} onchange={rename} />
			<Text bind:value={assistantName} label="Assistant" max={32} onchange={rename} />
		</div>
	</div>
	<FormSection title="Sampling">
		<Slider bind:value={() => read('temperature'), (n) => write('temperature', n)} min={0} max={2} step={0.05} label="Temperature" optional inline />
		<Slider bind:value={() => read('topP'), (n) => write('topP', n)} min={0} max={1} step={0.01} label="Top p" optional inline />
		<NumberField bind:value={() => read('topK'), (n) => write('topK', n)} min={1} label="Top k" optional inline />
		<NumberField bind:value={() => read('maxTokens'), (n) => write('maxTokens', n)} min={1} max={contextLength || undefined} label="Max tokens" optional inline />
		<NumberField bind:value={() => read('seed'), (n) => write('seed', n)} min={0} label="Seed" optional inline />
		<Chips bind:value={() => (settings.stop ? settings.stop.split('\n').filter(Boolean) : []), (v) => (settings.stop = v.join('\n'))} label="Stop sequences" />
	</FormSection>
	{#if serverOptions.length}
		<FormSection title="Tools">
			<Checklist bind:value={settings.toolServers} options={serverOptions} />
			{#if settings.toolServers.length}
				<Switch bind:checked={settings.approveTools} label="Ask before running a tool" />
				<Switch bind:checked={settings.mutatingTools} label="Let tools make changes" />
			{/if}
		</FormSection>
	{/if}
	<FormSection title="Request">
		<div class="pair">
			<FieldLabel label="API" />
			<Select bind:value={settings.dialect} options={apis} compact />
		</div>
		<Switch bind:checked={settings.stream} label="Stream" />
	</FormSection>
</div>

<style>
	/* Every boxed control starts on the second of two equal columns, where the assistant's name does */
	.sections {
		--nebu-inline-box: calc(50% - var(--nebu-space-3) / 2);

		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-5);
		min-width: 0;
	}
	.fields {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-3);
		min-width: 0;
	}
	.names,
	.pair {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--nebu-space-3);
		min-width: 0;
	}
	.pair {
		align-items: center;
	}
	/* The API names need the room the box keeps for a clear button it never shows */
	.pair :global(.neo-affix-container) {
		display: none;
	}
</style>
