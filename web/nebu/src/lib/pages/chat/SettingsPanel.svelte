<script lang="ts">
	import { shortId } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { ChatSettings } from '#lib/proto/nebu/v1/chat_pb.js';
	import { ToolServerState, type ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
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
	<FormSection title="Prompt">
		<Textarea bind:value={settings.system} label="System prompt" info="Sent before the first message of every request" rows={3} maxRows={12} />
	</FormSection>
	<FormSection title="Names" info="The names over the messages of this conversation, which the model never sees">
		<FieldRow>
			<Text bind:value={settings.userName} label="Yours" max={32} unset={userFallback} />
			<Text bind:value={settings.assistantName} label="The model's" max={32} unset={assistantFallback} />
		</FieldRow>
	</FormSection>
	<FormSection title="Sampling" info="A setting left empty is not sent, and the server answers with its own">
		<Slider bind:value={() => read('temperature'), (n) => write('temperature', n)} min={0} max={2} step={0.05} label="Temperature" resting="the server's" optional />
		<Slider bind:value={() => read('topP'), (n) => write('topP', n)} min={0} max={1} step={0.01} label="Top p" resting="the server's" optional />
		<FieldRow>
			<NumberField bind:value={() => read('topK'), (n) => write('topK', n)} min={1} label="Top k" unset="the server's" optional plain />
			<NumberField bind:value={() => read('seed'), (n) => write('seed', n)} min={0} label="Seed" unset="a new one each time" optional plain />
		</FieldRow>
		<NumberField bind:value={() => read('maxTokens'), (n) => write('maxTokens', n)} min={1} max={contextLength || undefined} unit="tokens" label="Longest answer" unset="the server's" optional plain />
		<Chips bind:value={() => (settings.stop ? settings.stop.split('\n').filter(Boolean) : []), (v) => (settings.stop = v.join('\n'))} label="Stop sequences" info="Text that ends an answer as soon as the model writes it" />
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
		<Select bind:value={settings.dialect} options={apis} label="API" info="The wire format the request is sent in" />
		<Switch bind:checked={settings.stream} label="Stream the answer as it is written" />
	</FormSection>
</div>

<style>
	.sections {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-5);
		min-width: 0;
	}
</style>
