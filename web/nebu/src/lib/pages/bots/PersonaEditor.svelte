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


{#snippet identityAdvanced()}
	<FieldRow>
		<Switch bind:checked={persona.webhook} label="Webhook" info="Posts through a channel webhook under the persona's name and avatar" />
		<Switch bind:checked={persona.stream} label="Stream" info="Edits the message while it generates" />
	</FieldRow>
{/snippet}

{#snippet samplingAdvanced()}
	<FieldRow>
		<NumberField value={sampling.topK ?? 0} min={0} label="Top k" info="Sampling pool size, the model's default when empty" emptyZero onchange={(n: number) => (sampling.topK = n || undefined)} />
		<NumberField value={sampling.maxTokens ?? 0} min={0} label="Max tokens" info="Longest answer, the model's default when empty" emptyZero onchange={(n: number) => (sampling.maxTokens = n || undefined)} />
	</FieldRow>
	<FieldRow>
		<Chips bind:value={sampling.stop} label="Stop" info="Sequences that end the answer" />
		<NumberField value={Number(sampling.seed ?? 0n)} label="Seed" info="Fixed sampling seed" emptyZero off={{ label: 'Drawn each request' }} onchange={(n: number) => (sampling.seed = n ? BigInt(Math.round(n)) : undefined)} />
	</FieldRow>
{/snippet}

{#snippet humanizeAdvanced()}
	<FieldRow wide>
		<Duration bind:value={humanize.maxTypingMs} base="ms" max={600_000} label="Max typing" info="Longest typing indicator" />
		<Slider bind:value={humanize.maxChunkChars} min={0} max={2000} step={10} label="Max chunk" info="Characters per message when splitting, the Discord limit when empty" unit="chars" blank={humanize.maxChunkChars === 0} />
	</FieldRow>
	<FieldRow>
		<Percent value={humanize.reactionChance * 100} label="Reaction chance" info="Share of messages that get an emoji reaction" onchange={(n: number) => (humanize.reactionChance = n / 100)} />
		<Chips bind:value={humanize.reactions} label="Reactions" info="Emojis drawn from at random" />
	</FieldRow>
	<FieldRow>
		<Percent value={humanize.ambientReplyChance * 100} label="Ambient reply chance" info="Share of unaddressed messages answered" onchange={(n: number) => (humanize.ambientReplyChance = n / 100)} />
		<Percent value={humanize.ignoreChance * 100} label="Ignore chance" info="Share of addressed messages left unanswered" onchange={(n: number) => (humanize.ignoreChance = n / 100)} />
	</FieldRow>
	<FieldRow>
		<Stack gap={4}>
			<FieldLabel label="Active hours" />
			<Row gap={8} wrap={false}>
				<Cell min="6rem"><Text value={activeFrom} type="time" placeholder="From" onchange={(v: string) => setHours(v, activeTo)} /></Cell>
				<Cell min="6rem"><Text value={activeTo} type="time" placeholder="To" onchange={(v: string) => setHours(activeFrom, v)} /></Cell>
			</Row>
		</Stack>
		<Select bind:value={() => humanize.timezone || undefined, (v) => (humanize.timezone = v ?? '')} options={timezones} label="Timezone" info="Zone the active hours are read in, the host's when empty" placeholder="Host's zone" search clearable />
	</FieldRow>
{/snippet}

<Stack gap={24}>
	<FormSection title="Identity" info="A webhook posts under the persona's own name and avatar instead of the bot's" advanced={identityAdvanced}>
		<FieldRow>
			<Text bind:value={persona.name} label="Name" info="The persona's display name" max={64} required />
			<Text bind:value={persona.avatarUrl} label="Avatar URL" info="Image shown as the webhook avatar" type="url" max={256} />
		</FieldRow>
		{#if persona.avatarUrl}<img src={persona.avatarUrl} alt="" width="48" height="48" />{/if}
		<Textarea bind:value={persona.systemPrompt} label="System prompt" info="Instructions every chat starts with" rows={4} maxRows={12} />
	</FormSection>
	<FormSection title="Models" info="An empty model takes the gateway's first ready route of that kind">
		<FieldRow>
			<Picker bind:value={() => persona.model || undefined, (v) => (persona.model = v ?? '')} fetch={textRoutes} label="Model" info="Chat route, the sole ready language model when empty" placeholder="Default route" clearable current={persona.model || undefined} />
			<Switch bind:checked={persona.vision} label="Vision" info="Passes message images to the language model" top />
		</FieldRow>
		<FieldRow>
			<Picker bind:value={() => persona.imageModel || undefined, (v) => (persona.imageModel = v ?? '')} fetch={imageRoutes} label="Image model" info="Image route, the sole ready image model when empty" placeholder="Default route" clearable current={persona.imageModel || undefined} />
			<Picker bind:value={() => persona.videoModel || undefined, (v) => (persona.videoModel = v ?? '')} fetch={videoRoutes} label="Video model" info="Video route, the sole ready video model when empty" placeholder="Default route" clearable current={persona.videoModel || undefined} />
		</FieldRow>
	</FormSection>
	<FormSection title="Sampling" info="How the model draws each token, an empty field leaving the model's own default" advanced={samplingAdvanced}>
		<FieldRow>
			<Slider value={sampling.temperature ?? 0} min={0} max={2} step={0.05} label="Temperature" info="Sampling randomness, the model's default when empty" blank={sampling.temperature === undefined} onchange={(n: number) => (sampling.temperature = n)} />
			<Slider value={sampling.topP ?? 0} min={0} max={1} step={0.01} label="Top p" info="Nucleus sampling share, the model's default when empty" blank={sampling.topP === undefined} onchange={(n: number) => (sampling.topP = n)} />
		</FieldRow>
	</FormSection>
	<FormSection title="Behaviour" info="Humanizing types at a human pace, splits long answers and drops the final period when casual" advanced={humanize.enabled ? humanizeAdvanced : undefined}>
		<FieldRow>
			<Switch bind:checked={humanize.enabled} label="Humanize" info="Types at a human pace with delays, splits and reactions" top />
			<Chips bind:value={persona.wakeWords} label="Wake words" info="Whole words that summon the persona, case ignored" />
		</FieldRow>
		{#if humanize.enabled}
			<FieldRow>
				<Slider bind:value={humanize.charsPerSecond} min={1} max={100} label="Characters per second" info="Typing speed the indicator is timed by" />
				<Switch bind:checked={humanize.splitMessages} label="Split messages" info="Splits long answers at paragraph and sentence ends" top />
			</FieldRow>
			<FieldRow wide>
				<Duration bind:value={humanize.delayMinMs} base="ms" max={600_000} label="Delay min" info="Shortest pause before typing" />
				<Duration bind:value={humanize.delayMaxMs} base="ms" max={600_000} label="Delay max" info="Longest pause before typing" />
			</FieldRow>
			<FieldRow>
				<Switch bind:checked={humanize.casual} label="Casual" info="Lowercases the answer and drops its final period" />
				<Switch bind:checked={humanize.quoteReply} label="Quote reply" info="Replies to the message, webhook personas mention the author instead" />
			</FieldRow>
		{/if}
	</FormSection>
	<FormSection title="Channels" info="Where the persona speaks up without being asked">
		<ChannelTree bind:value={persona.channelIds} {guilds} label="Unprompted replies" info="Channels for ambient replies, every allowed one when empty" />
	</FormSection>
	<FormSection title="Media" info="Words added to every image and video the persona makes">
		<FieldRow>
			<Textarea bind:value={persona.imageStyle} label="Image style" info="Appended to every image prompt" rows={2} />
			<Textarea bind:value={persona.negativePrompt} label="Negative prompt" info="What images and video should avoid" rows={2} />
		</FieldRow>
	</FormSection>
	<FormSection title="Tools" info="Tool servers the persona's chats may call">
		<FieldRow>
			<Checklist bind:value={persona.toolServers} options={serverOptions} label="Tool servers" info="Servers whose tools the persona's chats get" />
			<Switch bind:checked={persona.mutatingTools} label="Mutating tools" info="Offers tools that change what they touch, read only tools alone otherwise" top />
		</FieldRow>
	</FormSection>
</Stack>
