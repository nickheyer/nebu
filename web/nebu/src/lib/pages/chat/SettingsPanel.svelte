<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import Collapse from '#lib/ui/Collapse.svelte';
	import { shortId } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { ChatSettings } from '#lib/proto/nebu/v1/chat_pb.js';
	import { ToolServerState, type ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import Code from '#lib/ui/fields/Code.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Slider from '#lib/ui/fields/Slider.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Textarea from '#lib/ui/fields/Textarea.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';

	let {
		settings = $bindable(),
		servers,
		serversError,
		// Tokens the picked route holds, zero when the route does not say
		contextLength
	}: { settings: ChatSettings; servers: ToolServerStatus[]; serversError: string; contextLength: number } = $props();

	type NumKey = 'temperature' | 'topP' | 'topK' | 'maxTokens' | 'seed';

	const unset = 'Runtime default';
	const dialects = [
		{ value: 'openai', label: 'OpenAI' },
		{ value: 'anthropic', label: 'Anthropic' },
		{ value: 'ollama', label: 'Ollama' }
	];

	// The stored number, the idle value standing in while the setting is unset
	function read(key: NumKey, idle = 0): number {
		const v = settings[key] === '' ? NaN : globalThis.Number(settings[key]);
		return globalThis.Number.isNaN(v) ? idle : v;
	}

	// Stores a number, a write of the idle value while unset keeping the runtime default
	function write(key: NumKey, n: number, idle = 0) {
		if (settings[key] === '' && n === idle) return;
		settings[key] = String(n);
	}

	// Stores a number whose zero means unset, the request then leaving it out
	function writeOptional(key: NumKey, n: number) {
		settings[key] = n ? String(n) : '';
	}

	function resetSampling() {
		settings.temperature = '';
		settings.topP = '';
		settings.topK = '';
		settings.maxTokens = '';
		settings.seed = '';
		settings.stop = '';
	}

	const serverOptions = $derived(
		servers.map((s) => {
			const value = s.ref || s.server?.name || '';
			const node = s.nodeId ? live.nodes.get(s.nodeId)?.name || shortId(s.nodeId) : undefined;
			const failed = s.state === ToolServerState.FAILED;
			return { value, label: s.server?.name || value, description: node, disabled: failed, tip: failed ? s.error : undefined };
		})
	);
</script>

<Stack gap={16}>
	<Segmented bind:value={settings.dialect} options={dialects} label="Dialect" />
	<Textarea bind:value={settings.system} label="System" rows={3} maxRows={12} />
	<Collapse label="Sampling">
		<Stack gap={12}>
			<FieldRow>
				<Switch bind:checked={settings.stream} label="Stream" />
				<NumberField value={read('seed')} label="Seed" placeholder="Random" emptyZero onchange={(n) => writeOptional('seed', n)} />
			</FieldRow>
			<FieldRow>
				<Slider bind:value={() => read('temperature'), (n) => write('temperature', n)} min={0} max={2} step={0.05} label="Temperature" message={settings.temperature === '' ? unset : undefined} />
				<Slider bind:value={() => read('topP', 1), (n) => write('topP', n, 1)} min={0} max={1} step={0.01} label="Top p" message={settings.topP === '' ? unset : undefined} />
			</FieldRow>
			<FieldRow>
				<NumberField value={read('topK')} min={0} label="Top k" placeholder={unset} emptyZero onchange={(n) => writeOptional('topK', n)} />
				{#if contextLength > 0}
					<Slider bind:value={() => read('maxTokens', contextLength), (n) => write('maxTokens', n, contextLength)} min={1} max={contextLength} label="Max tokens" message={settings.maxTokens === '' ? unset : undefined} />
				{:else}
					<NumberField value={read('maxTokens')} min={0} label="Max tokens" placeholder={unset} emptyZero onchange={(n) => writeOptional('maxTokens', n)} />
				{/if}
			</FieldRow>
			<Chips bind:value={() => (settings.stop ? settings.stop.split('\n').filter(Boolean) : []), (v) => (settings.stop = v.join('\n'))} label="Stop" />
			<Row justify="flex-end">
				<NeoButton text onclick={resetSampling}>Reset</NeoButton>
			</Row>
		</Stack>
	</Collapse>
	<Collapse label="Tools">
		<Stack gap={12}>
			<Checklist bind:value={settings.toolServers} options={serverOptions} label="Tool servers" empty={serversError || 'No tool servers'} />
			<FieldRow>
				<Switch bind:checked={settings.mutatingTools} label="Mutating tools" />
				<Switch bind:checked={settings.approveTools} label="Approve tools" info="Calls wait for Run in the chat" />
			</FieldRow>
			<Code bind:value={settings.tools} language="json" rows={6} label="Custom tools" info="JSON array of client tools" />
		</Stack>
	</Collapse>
	<Collapse label="Names">
		<FieldRow>
			<Text bind:value={settings.userName} label="User name" max={32} placeholder="You" />
			<Text bind:value={settings.assistantName} label="Assistant name" max={32} placeholder="Model name" />
		</FieldRow>
	</Collapse>
</Stack>
