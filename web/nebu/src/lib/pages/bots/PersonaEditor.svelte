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


{#snippet samplingAdvanced()}
	<FieldRow>
		<NumberField bind:value={sampling.topK} min={1} label="Top k" optional />
		<NumberField bind:value={sampling.maxTokens} min={1} label="Max tokens" optional />
	</FieldRow>
	<FieldRow>
		<Chips bind:value={sampling.stop} label="Stop sequences" />
		<NumberField bind:value={() => (sampling.seed === undefined ? undefined : Number(sampling.seed)), (n) => (sampling.seed = n === undefined ? undefined : BigInt(Math.round(n)))} min={0} label="Seed" optional />
	</FieldRow>
{/snippet}

{#snippet humanizeAdvanced()}
	<FieldRow wide>
		<Duration bind:value={humanize.maxTypingMs} base="ms" max={600_000} label="Longest typing" />
		<NumberField bind:value={humanize.maxChunkChars} min={0} max={2000} step={10} label="Message length" info="Characters before an answer is split" unit="chars" emptyZero fallback={2000} />
	</FieldRow>
	<FieldRow>
		<Percent value={humanize.reactionChance * 100} label="Reaction chance" onchange={(n: number) => (humanize.reactionChance = n / 100)} />
		<Chips bind:value={humanize.reactions} label="Reactions" />
	</FieldRow>
	<FieldRow>
		<Percent value={humanize.ambientReplyChance * 100} label="Unprompted reply chance" onchange={(n: number) => (humanize.ambientReplyChance = n / 100)} />
		<Percent value={humanize.ignoreChance * 100} label="Ignore chance" info="Share of messages to the persona left unanswered" onchange={(n: number) => (humanize.ignoreChance = n / 100)} />
	</FieldRow>
	<FieldRow>
		<Stack gap={4}>
			<FieldLabel label="Active hours" />
			<Row gap={8} wrap={false}>
				<Cell min="6rem"><Text value={activeFrom} type="time" onchange={(v: string) => setHours(v, activeTo)} /></Cell>
				<Cell min="6rem"><Text value={activeTo} type="time" onchange={(v: string) => setHours(activeFrom, v)} /></Cell>
			</Row>
		</Stack>
		<Select bind:value={() => humanize.timezone || undefined, (v) => (humanize.timezone = v ?? '')} options={timezones} label="Timezone" search clearable />
	</FieldRow>
{/snippet}

<Stack gap={24}>
	<FormSection>
		<FieldRow>
			<Text bind:value={persona.name} label="Name" max={64} required />
			<Text bind:value={persona.avatarUrl} label="Avatar URL" type="url" max={256} />
		</FieldRow>
		{#if persona.avatarUrl}<img src={persona.avatarUrl} alt="" width="48" height="48" />{/if}
		<Textarea bind:value={persona.systemPrompt} label="System prompt" rows={4} maxRows={12} />
		<FieldRow>
			<Switch bind:checked={persona.webhook} label="Post through a webhook" info="Shows the persona's own name and avatar" />
			<Switch bind:checked={persona.stream} label="Stream" />
		</FieldRow>
	</FormSection>
	<FormSection title="Models">
		<FieldRow>
			<Picker bind:value={() => persona.model || undefined, (v) => (persona.model = v ?? '')} fetch={textRoutes} label="Chat" clearable current={persona.model || undefined} />
			<Switch bind:checked={persona.vision} label="Send images to the model" />
		</FieldRow>
		<FieldRow>
			<Picker bind:value={() => persona.imageModel || undefined, (v) => (persona.imageModel = v ?? '')} fetch={imageRoutes} label="Images" clearable current={persona.imageModel || undefined} />
			<Picker bind:value={() => persona.videoModel || undefined, (v) => (persona.videoModel = v ?? '')} fetch={videoRoutes} label="Video" clearable current={persona.videoModel || undefined} />
		</FieldRow>
	</FormSection>
	<FormSection title="Sampling" advanced={samplingAdvanced}>
		<FieldRow>
			<Slider bind:value={sampling.temperature} min={0} max={2} step={0.05} label="Temperature" optional />
			<Slider bind:value={sampling.topP} min={0} max={1} step={0.01} label="Top p" optional />
		</FieldRow>
	</FormSection>
	<FormSection title="Behaviour" advanced={humanize.enabled ? humanizeAdvanced : undefined}>
		<FieldRow>
			<Switch bind:checked={humanize.enabled} label="Type like a person" info="Pauses, typing speed, split messages and reactions" />
			<Chips bind:value={persona.wakeWords} label="Wake words" info="Words that call the persona" />
		</FieldRow>
		{#if humanize.enabled}
			<FieldRow>
				<NumberField bind:value={humanize.charsPerSecond} min={1} max={100} label="Typing speed" unit="chars/s" />
				<Switch bind:checked={humanize.splitMessages} label="Split long answers" />
			</FieldRow>
			<FieldRow wide>
				<Duration bind:value={humanize.delayMinMs} base="ms" max={600_000} label="Shortest pause" />
				<Duration bind:value={humanize.delayMaxMs} base="ms" max={600_000} label="Longest pause" />
			</FieldRow>
			<FieldRow>
				<Switch bind:checked={humanize.casual} label="Casual" info="Lowercase, no final period" />
				<Switch bind:checked={humanize.quoteReply} label="Reply to the message" />
			</FieldRow>
		{/if}
	</FormSection>
	{#if guilds.length}
		<FormSection title="Unprompted replies" info="Every channel when none is checked">
			<ChannelTree bind:value={persona.channelIds} {guilds} />
		</FormSection>
	{/if}
	<FormSection title="Media">
		<FieldRow>
			<Textarea bind:value={persona.imageStyle} label="Image style" info="Added to every image prompt" rows={2} />
			<Textarea bind:value={persona.negativePrompt} label="Negative prompt" rows={2} />
		</FieldRow>
	</FormSection>
	{#if serverOptions.length}
		<FormSection title="Tools">
			<Checklist bind:value={persona.toolServers} options={serverOptions} />
			{#if persona.toolServers.length}
				<Switch bind:checked={persona.mutatingTools} label="Let tools make changes" />
			{/if}
		</FormSection>
	{/if}
</Stack>
