<script lang="ts">
	import { untrack } from 'svelte';
	import { shortId } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { ChatSettings } from '#lib/proto/nebu/v1/chat_pb.js';
	import { ToolServerState, type ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Slider from '#lib/ui/fields/Slider.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Textarea from '#lib/ui/fields/Textarea.svelte';
	import type { Option } from '#lib/ui/fields/types.js';

	// The request settings of one conversation, every empty field showing the value the request resolves to
	let {
		settings = $bindable(),
		servers,
		contextLength,
		userFallback,
		assistantFallback
	}: { settings: ChatSettings; servers: ToolServerStatus[]; contextLength: number; userFallback: string; assistantFallback: string } = $props();

	type NumKey = 'temperature' | 'topP' | 'topK' | 'maxTokens' | 'seed';

	// The sampling each API's server applies while a setting stays unset
	const apiDefaults: Record<string, { temperature: number; topP?: number; topK?: number }> = {
		openai: { temperature: 1, topP: 1 },
		anthropic: { temperature: 1 },
		ollama: { temperature: 0.8, topP: 0.9, topK: 40 }
	};
	const d = $derived(apiDefaults[settings.dialect] ?? apiDefaults.openai!);

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

<div class="sections">
	<Segmented bind:value={settings.dialect} options={apis} label="API" />
	<Switch bind:checked={settings.stream} label="Stream the answer" />
	<Textarea bind:value={settings.system} label="System prompt" rows={3} maxRows={12} />
	<FieldRow>
		<Text bind:value={userName} label="User's name" max={32} onchange={rename} />
		<Text bind:value={assistantName} label="Assistant's name" max={32} onchange={rename} />
	</FieldRow>
	<FormSection title="Sampling">
		<Slider bind:value={() => read('temperature'), (n) => write('temperature', n)} min={0} max={2} step={0.05} label="Temperature" fallback={d.temperature} optional />
		<Slider bind:value={() => read('topP'), (n) => write('topP', n)} min={0} max={1} step={0.01} label="Top p" fallback={d.topP} optional />
		<NumberField bind:value={() => read('topK'), (n) => write('topK', n)} min={1} label="Top k" fallback={d.topK} optional />
		<NumberField bind:value={() => read('maxTokens'), (n) => write('maxTokens', n)} min={1} max={contextLength || undefined} label="Max tokens" fallback={contextLength || undefined} optional />
		<NumberField bind:value={() => read('seed'), (n) => write('seed', n)} min={0} label="Seed" optional />
		<Chips bind:value={() => (settings.stop ? settings.stop.split('\n').filter(Boolean) : []), (v) => (settings.stop = v.join('\n'))} label="Stop sequences" />
	</FormSection>
	{#if serverOptions.length}
		<FormSection title="Tools">
			<Checklist bind:value={settings.toolServers} options={serverOptions} />
			{#if settings.toolServers.length}
				<Switch bind:checked={settings.mutatingTools} label="Let tools make changes" />
				<Switch bind:checked={settings.approveTools} label="Ask before running a tool" />
			{/if}
		</FormSection>
	{/if}
</div>

<style>
	.sections {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-4);
		min-width: 0;
	}
</style>
