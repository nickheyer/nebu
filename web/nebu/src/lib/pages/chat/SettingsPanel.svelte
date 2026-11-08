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
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Slider from '#lib/ui/fields/Slider.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Textarea from '#lib/ui/fields/Textarea.svelte';

	let {
		settings = $bindable(),
		servers,
		serversError,
		// Tokens the picked route holds, zero when the route does not say
		contextLength
	}: { settings: ChatSettings; servers: ToolServerStatus[]; serversError: string; contextLength: number } = $props();

	type NumKey = 'temperature' | 'topP' | 'topK' | 'maxTokens' | 'seed';

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

	const sampled = $derived(settings.temperature !== '' || settings.topP !== '' || settings.topK !== '' || settings.maxTokens !== '' || settings.seed !== '' || settings.stop !== '');

	const serverOptions = $derived(
		servers.map((s) => {
			const value = s.ref || s.server?.name || '';
			const node = s.nodeId ? live.nodes.get(s.nodeId)?.name || shortId(s.nodeId) : undefined;
			const failed = s.state === ToolServerState.FAILED;
			return { value, label: s.server?.name || value, description: node, disabled: failed, tip: failed ? s.error : undefined };
		})
	);
</script>

<div class="sections">
	<Segmented bind:value={settings.dialect} options={dialects} label="Dialect" />
	<Textarea bind:value={settings.system} label="System" rows={3} maxRows={12} />
	<Collapse label="Sampling">
		<div class="fields">
			<Switch bind:checked={settings.stream} label="Stream" />
			<Slider bind:value={() => read('temperature'), (n) => write('temperature', n)} min={0} max={2} step={0.05} label="Temperature" blank={settings.temperature === ''} placeholder="Default" />
			<Slider bind:value={() => read('topP', 1), (n) => write('topP', n, 1)} min={0} max={1} step={0.01} label="Top p" blank={settings.topP === ''} placeholder="Default" />
			{#if contextLength > 0}
				<Slider bind:value={() => read('maxTokens', contextLength), (n) => write('maxTokens', n, contextLength)} min={1} max={contextLength} label="Max tokens" blank={settings.maxTokens === ''} placeholder="Default" />
			{:else}
				<NumberField value={read('maxTokens')} min={0} label="Max tokens" placeholder="Default" emptyZero onchange={(n) => writeOptional('maxTokens', n)} />
			{/if}
			<NumberField value={read('topK')} min={0} label="Top k" placeholder="Default" emptyZero onchange={(n) => writeOptional('topK', n)} />
			<NumberField value={read('seed')} label="Seed" placeholder="Random" emptyZero onchange={(n) => writeOptional('seed', n)} />
			<Chips bind:value={() => (settings.stop ? settings.stop.split('\n').filter(Boolean) : []), (v) => (settings.stop = v.join('\n'))} label="Stop" />
			<div class="reset">
				<NeoButton text disabled={!sampled} onclick={resetSampling}>Reset to defaults</NeoButton>
			</div>
		</div>
	</Collapse>
	<Collapse label="Tools">
		<div class="fields">
			<Checklist bind:value={settings.toolServers} options={serverOptions} label="Tool servers" empty={serversError || 'No tool servers'} />
			<Switch bind:checked={settings.mutatingTools} label="Mutating tools" />
			<Switch bind:checked={settings.approveTools} label="Approve tools" info="Calls wait for Run in the chat" />
			<Code bind:value={settings.tools} language="json" rows={6} label="Custom tools" info="JSON array of client tools" />
		</div>
	</Collapse>
	<Collapse label="Names">
		<div class="fields">
			<Text bind:value={settings.userName} label="User name" max={32} placeholder="You" />
			<Text bind:value={settings.assistantName} label="Assistant name" max={32} placeholder="Model name" />
		</div>
	</Collapse>
</div>

<style>
	.sections {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-4);
		min-width: 0;
	}
	.fields {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-4);
		min-width: 0;
	}
	.reset {
		display: flex;
		justify-content: flex-end;
	}
</style>
