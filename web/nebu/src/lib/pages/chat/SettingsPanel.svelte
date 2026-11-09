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
		contextLength,
		assistantFallback
	}: { settings: ChatSettings; servers: ToolServerStatus[]; serversError: string; contextLength: number; assistantFallback: string } = $props();

	type NumKey = 'temperature' | 'topP' | 'topK' | 'maxTokens' | 'seed';

	// The sampling each dialect's server applies while a setting stays unset
	const dialectDefaults: Record<string, { temperature: number; topP?: number; topK?: number }> = {
		openai: { temperature: 1, topP: 1 },
		anthropic: { temperature: 1 },
		ollama: { temperature: 0.8, topP: 0.9, topK: 40 }
	};
	const d = $derived(dialectDefaults[settings.dialect] ?? dialectDefaults.openai!);

	const dialects = [
		{ value: 'openai', label: 'OpenAI' },
		{ value: 'anthropic', label: 'Anthropic' },
		{ value: 'ollama', label: 'Ollama' }
	];

	function read(key: NumKey, idle = 0): number {
		const v = settings[key] === '' ? NaN : globalThis.Number(settings[key]);
		return globalThis.Number.isNaN(v) ? idle : v;
	}

	// Stores a number, the idle value written while unset keeping it unset
	function write(key: NumKey, n: number, idle = 0) {
		if (settings[key] === '' && n === idle) return;
		settings[key] = String(n);
	}

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
	<Segmented bind:value={settings.dialect} options={dialects} label="Dialect" info="Wire format of the requests, OpenAI, Anthropic or Ollama" />
	<Textarea bind:value={settings.system} label="System" info="Instructions the conversation starts with" rows={3} maxRows={12} />
	<Collapse label="Sampling">
		<div class="fields">
			<Switch bind:checked={settings.stream} label="Stream" info="Shows tokens as they arrive" />
			<Slider bind:value={() => read('temperature', d.temperature), (n) => write('temperature', n, d.temperature)} min={0} max={2} step={0.05} label="Temperature" info="Sampling randomness, the dialect's default when empty" blank={settings.temperature === ''} fallback={d.temperature} />
			<Slider bind:value={() => read('topP', d.topP ?? 1), (n) => write('topP', n, d.topP ?? 1)} min={0} max={1} step={0.01} label="Top p" info="Nucleus sampling share, the dialect's default when empty" blank={settings.topP === ''} fallback={d.topP} />
			{#if contextLength > 0}
				<Slider bind:value={() => read('maxTokens', contextLength), (n) => write('maxTokens', n, contextLength)} min={1} max={contextLength} label="Max tokens" info="Longest answer, the context length when empty" blank={settings.maxTokens === ''} fallback={contextLength} />
			{:else}
				<NumberField value={read('maxTokens')} min={0} label="Max tokens" info="Longest answer, the model's default when empty" emptyZero onchange={(n) => writeOptional('maxTokens', n)} />
			{/if}
			<NumberField value={read('topK')} min={0} label="Top k" info="Sampling pool size, the dialect's default when empty" placeholder={d.topK ? String(d.topK) : undefined} emptyZero onchange={(n) => writeOptional('topK', n)} />
			<NumberField value={read('seed')} label="Seed" info="Fixed sampling seed" emptyZero off={{ label: 'Drawn each request' }} onchange={(n) => writeOptional('seed', n)} />
			<Chips bind:value={() => (settings.stop ? settings.stop.split('\n').filter(Boolean) : []), (v) => (settings.stop = v.join('\n'))} label="Stop" info="Sequences that end the answer" />
			<div class="reset">
				<NeoButton text disabled={!sampled} onclick={resetSampling}>Reset</NeoButton>
			</div>
		</div>
	</Collapse>
	<Collapse label="Tools">
		<div class="fields">
			<Checklist bind:value={settings.toolServers} options={serverOptions} label="Tool servers" info="Servers whose tools the model may call" empty={serversError || undefined} />
			<Switch bind:checked={settings.mutatingTools} label="Mutating tools" info="Offers tools that change what they touch, read only tools alone otherwise" />
			<Switch bind:checked={settings.approveTools} label="Approve tools" info="Gateway tools wait for your approval before they run" />
			<Code bind:value={settings.tools} language="json" rows={6} label="Client tools" info="Tool definitions as a JSON array, each call answered here by hand" />
		</div>
	</Collapse>
	<Collapse label="Names">
		<div class="fields">
			<Text bind:value={settings.userName} label="User name" info="Shown over your turns" max={32} placeholder="You" />
			<Text bind:value={settings.assistantName} label="Assistant name" info="Shown over the model's turns, the model name when empty" max={32} placeholder={assistantFallback} />
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
