<script lang="ts">
	import { untrack } from 'svelte';
	import type { BotGuild, Persona } from '#lib/proto/nebu/v1/bot_pb.js';
	import { ToolServerState, type ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Percent from '#lib/ui/fields/Percent.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Slider from '#lib/ui/fields/Slider.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Textarea from '#lib/ui/fields/Textarea.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import ChannelTree from '#lib/pages/bots/ChannelTree.svelte';
	import { routeFetch, timezones } from '#lib/pages/bots/bots.js';

	let { persona = $bindable(), guilds, servers }: { persona: Persona; guilds: BotGuild[]; servers: ToolServerStatus[] } = $props();

	const sampling = $derived(persona.sampling!);
	const humanize = $derived(persona.humanize!);
	let activeFrom = $state(untrack(() => persona.humanize?.activeHours.split('-')[0] ?? ''));
	let activeTo = $state(untrack(() => persona.humanize?.activeHours.split('-')[1] ?? ''));

	// Writes both ends of the active window, empty unless both are set
	function setHours(from: string, to: string) {
		activeFrom = from;
		activeTo = to;
		humanize.activeHours = from && to ? `${from}-${to}` : '';
	}

	const serverOptions = $derived(
		servers.map((t) => {
			const ref = t.ref || t.server?.name || '';
			return { value: ref, label: ref, disabled: t.state === ToolServerState.FAILED, tip: t.state === ToolServerState.FAILED ? t.error : undefined };
		})
	);
	const textRoutes = routeFetch('text');
	const imageRoutes = routeFetch('image');
	const videoRoutes = routeFetch('video');
</script>

{#snippet identityAdvanced()}
	<FieldRow>
		<Switch bind:checked={persona.webhook} label="Webhook" info="Posts under the persona's name and avatar" />
		<Switch bind:checked={persona.stream} label="Stream" info="Edits the message while generating" />
	</FieldRow>
{/snippet}

{#snippet samplingAdvanced()}
	<FieldRow>
		<NumberField value={sampling.topK ?? 0} min={0} label="Top k" placeholder="Default" emptyZero onchange={(n: number) => (sampling.topK = n || undefined)} />
		<NumberField value={sampling.maxTokens ?? 0} min={0} label="Max tokens" placeholder="Default" emptyZero onchange={(n: number) => (sampling.maxTokens = n || undefined)} />
	</FieldRow>
	<FieldRow>
		<Chips bind:value={sampling.stop} label="Stop" />
		<NumberField value={Number(sampling.seed ?? 0n)} label="Seed" placeholder="Random" emptyZero onchange={(n: number) => (sampling.seed = n ? BigInt(Math.round(n)) : undefined)} />
	</FieldRow>
{/snippet}

{#snippet humanizeAdvanced()}
	<FieldRow>
		<Duration bind:value={humanize.maxTypingMs} base="ms" max={600_000} label="Max typing" />
		<Slider bind:value={humanize.maxChunkChars} min={0} max={2000} step={10} label="Max chunk" unit="chars" info="0 is the Discord limit" />
	</FieldRow>
	<FieldRow>
		<Percent value={humanize.reactionChance * 100} label="Reaction chance" onchange={(n: number) => (humanize.reactionChance = n / 100)} />
		<Chips bind:value={humanize.reactions} label="Reactions" placeholder="Add an emoji" />
	</FieldRow>
	<FieldRow>
		<Percent value={humanize.ambientReplyChance * 100} label="Ambient reply chance" onchange={(n: number) => (humanize.ambientReplyChance = n / 100)} />
		<Percent value={humanize.ignoreChance * 100} label="Ignore chance" onchange={(n: number) => (humanize.ignoreChance = n / 100)} />
	</FieldRow>
	<FieldRow>
		<Stack gap={4}>
			<FieldLabel label="Active hours" info="Empty is always" />
			<Row gap={8} wrap={false}>
				<Cell min="6rem"><Text value={activeFrom} type="time" placeholder="From" onchange={(v: string) => setHours(v, activeTo)} /></Cell>
				<Cell min="6rem"><Text value={activeTo} type="time" placeholder="To" onchange={(v: string) => setHours(activeFrom, v)} /></Cell>
			</Row>
		</Stack>
		<Select bind:value={() => humanize.timezone || undefined, (v) => (humanize.timezone = v ?? '')} options={timezones} label="Timezone" search clearable placeholder="Host timezone" />
	</FieldRow>
{/snippet}

<Stack gap={24}>
	<FormSection title="Identity" advanced={identityAdvanced}>
		<FieldRow>
			<Text bind:value={persona.name} label="Name" max={64} required />
			<Text bind:value={persona.avatarUrl} label="Avatar URL" type="url" max={256} />
		</FieldRow>
		{#if persona.avatarUrl}<img src={persona.avatarUrl} alt="" width="48" height="48" />{/if}
		<Textarea bind:value={persona.systemPrompt} label="System prompt" rows={4} maxRows={12} />
	</FormSection>
	<FormSection title="Models">
		<FieldRow>
			<Picker bind:value={() => persona.model || undefined, (v) => (persona.model = v ?? '')} fetch={textRoutes} label="Model" placeholder="Sole ready route" clearable current={persona.model || undefined} />
			<Switch bind:checked={persona.vision} label="Vision" info="Passes message images to the model" />
		</FieldRow>
		<FieldRow>
			<Picker bind:value={() => persona.imageModel || undefined, (v) => (persona.imageModel = v ?? '')} fetch={imageRoutes} label="Image model" placeholder="Sole ready route" clearable current={persona.imageModel || undefined} />
			<Picker bind:value={() => persona.videoModel || undefined, (v) => (persona.videoModel = v ?? '')} fetch={videoRoutes} label="Video model" placeholder="Sole ready route" clearable current={persona.videoModel || undefined} />
		</FieldRow>
	</FormSection>
	<FormSection title="Sampling" advanced={samplingAdvanced}>
		<FieldRow>
			<Slider value={sampling.temperature ?? 0} min={0} max={2} step={0.05} label="Temperature" blank={sampling.temperature === undefined} placeholder="Default" onchange={(n: number) => (sampling.temperature = n)} />
			<Slider value={sampling.topP ?? 0} min={0} max={1} step={0.01} label="Top p" blank={sampling.topP === undefined} placeholder="Default" onchange={(n: number) => (sampling.topP = n)} />
		</FieldRow>
	</FormSection>
	<FormSection title="Behaviour" advanced={humanize.enabled ? humanizeAdvanced : undefined}>
		<FieldRow>
			<Switch bind:checked={humanize.enabled} label="Humanize" info="Types, pauses and splits like a person" />
			<Chips bind:value={persona.wakeWords} label="Wake words" />
		</FieldRow>
		{#if humanize.enabled}
			<FieldRow>
				<Slider bind:value={humanize.charsPerSecond} min={1} max={100} label="Characters per second" />
				<Switch bind:checked={humanize.splitMessages} label="Split messages" />
			</FieldRow>
			<FieldRow>
				<Duration bind:value={humanize.delayMinMs} base="ms" max={600_000} label="Delay min" />
				<Duration bind:value={humanize.delayMaxMs} base="ms" max={600_000} label="Delay max" />
			</FieldRow>
			<FieldRow>
				<Switch bind:checked={humanize.casual} label="Casual" info="Lowercase, no final period" />
				<Switch bind:checked={humanize.quoteReply} label="Quote reply" />
			</FieldRow>
		{/if}
	</FormSection>
	<FormSection title="Channels">
		<ChannelTree bind:value={persona.channelIds} {guilds} label="Unprompted replies" info="Empty is every allowed channel" />
	</FormSection>
	<FormSection title="Media">
		<FieldRow>
			<Textarea bind:value={persona.imageStyle} label="Image style" rows={2} />
			<Textarea bind:value={persona.negativePrompt} label="Negative prompt" rows={2} />
		</FieldRow>
	</FormSection>
	<FormSection title="Tools">
		<FieldRow>
			<Checklist bind:value={persona.toolServers} options={serverOptions} label="Tool servers" empty="No tool servers" />
			<Switch bind:checked={persona.mutatingTools} label="Mutating tools" />
		</FieldRow>
	</FormSection>
</Stack>
