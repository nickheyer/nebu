<script lang="ts">
	import { untrack } from 'svelte';
	import type { BotGuild, BotSpec } from '#lib/proto/nebu/v1/bot_pb.js';
	import BytesField from '#lib/ui/fields/Bytes.svelte';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import KeyValues from '#lib/ui/fields/KeyValues.svelte';
	import MultiSelect from '#lib/ui/fields/MultiSelect.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Slider from '#lib/ui/fields/Slider.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import ChannelTree from '#lib/pages/bots/ChannelTree.svelte';
	import { activityTypes, commandRoles, fpsOptions, guildOptions, imageSizes, presenceStatuses, videoSizes } from '#lib/pages/bots/bots.js';

	let { spec = $bindable(), guilds, recommendedShards }: { spec: BotSpec; guilds: BotGuild[]; recommendedShards: number } = $props();

	const engagement = $derived(spec.engagement!);
	const commands = $derived(spec.commands!);
	const memory = $derived(spec.memory!);
	const media = $derived(spec.media!);
	const presence = $derived(spec.presence!);
	const guildChoices = $derived(guildOptions(guilds));
	const guildsEmpty = $derived(guilds.length ? undefined : 'Not connected');

	// Every role keeps a row, a blank name falling back to the role
	let names = $state<Record<string, string>>(untrack(() => Object.fromEntries(commandRoles.map((r) => [r, spec.commands?.names[r] || r]))));
	$effect(() => {
		const next = Object.fromEntries(commandRoles.map((r) => [r, names[r]?.trim() || r]));
		const stored = untrack(() => commands.names);
		if (commandRoles.some((r) => next[r] !== (stored[r] || r))) commands.names = next;
	});
</script>

<!-- Sections sit in pairs on one grid, so each pair starts on the same line across the gutter -->
<div class="wrap">
	<div class="pairs">
		<FormSection title="Engagement" info="A required mention means a mention, reply or wake word in guilds, member events need the privileged intent">
			<FieldRow>
				<Switch bind:checked={engagement.directMessages} label="Direct messages" info="Answers direct messages" />
				<Switch bind:checked={engagement.requireMention} label="Require mention" info="In servers, answers only a mention, reply or wake word" />
			</FieldRow>
			<FieldRow>
				<Switch bind:checked={engagement.answerBots} label="Answer bots" info="Replies to bots and webhooks, this bot's own personas included" />
				<Switch bind:checked={engagement.memberEvents} label="Member events" info="Hears joins and leaves, needs the privileged members intent" />
			</FieldRow>
			<Checklist bind:value={engagement.guildIds} options={guildChoices} label="Guilds" info="Servers the bot answers in, every server when empty" empty={guildsEmpty} />
			<FieldRow>
				<ChannelTree bind:value={engagement.channelIds} {guilds} label="Channels" info="Channels the bot answers in, every channel when empty" />
				<ChannelTree bind:value={engagement.deniedChannelIds} {guilds} label="Denied channels" info="Channels the bot never answers in" />
			</FieldRow>
			<FieldRow>
				<Chips bind:value={engagement.userIds} label="Users" info="Users the bot answers, everyone when empty" numeric />
				<Chips bind:value={engagement.deniedUserIds} label="Denied users" info="Users the bot never answers" numeric />
			</FieldRow>
			<FieldRow>
				<NumberField bind:value={engagement.maxBotChain} min={0} label="Max bot chain" info="Consecutive bot messages before it stops answering" placeholder="3" emptyZero />
				<NumberField bind:value={engagement.maxConcurrent} min={0} label="Max concurrent" info="Generations running at once" placeholder="4" emptyZero />
			</FieldRow>
			<FieldRow wide>
				<Duration bind:value={engagement.cooldownMs} base="ms" smallest="s" max={3_600_000} label="Cooldown" info="Least time between answers in one channel" placeholder="None" emptyZero />
				<Duration bind:value={engagement.userCooldownMs} base="ms" smallest="s" max={3_600_000} label="User cooldown" info="Least time between answers to one person" placeholder="None" emptyZero />
			</FieldRow>
		</FormSection>
		<FormSection title="Commands" info="Slash commands and the text prefix the bot answers to">
			<FieldRow>
				<Switch bind:checked={commands.enabled} label="Commands" info="Registers slash commands" top />
				<Text bind:value={engagement.prefix} label="Prefix" info="Character that starts a text command, such as !" placeholder="None" max={1} />
			</FieldRow>
			<FieldRow>
				<Checklist bind:value={commands.guildIds} options={guildChoices} label="Register in guilds" info="Servers that get the commands at once, global registration otherwise and that can take an hour" empty={guildsEmpty} />
				<MultiSelect bind:value={commands.disabled} options={commandRoles.map((r) => ({ value: r, label: r }))} label="Disabled commands" info="Command roles left out" placeholder="None" />
			</FieldRow>
			<KeyValues bind:value={names} label="Command names" info="The name each command role registers under" fixedKeys keyLabel="Role" valueLabel="Name" />
		</FormSection>
		<FormSection title="Memory" info="The history sent with each prompt, trimmed by count, size and age">
			<FieldRow>
				<NumberField bind:value={memory.messages} min={0} label="Messages" info="History messages sent with each prompt" placeholder="20" emptyZero />
				<NumberField bind:value={memory.maxChars} min={0} label="Max characters" info="History size cap, oldest messages dropped first" placeholder="8000" emptyZero />
			</FieldRow>
			<FieldRow wide>
				<Duration bind:value={() => memory.windowMinutes * 60, (s) => (memory.windowMinutes = Math.round(s / 60))} base="s" max={604_800} label="Window" info="Oldest history sent" emptyZero off={{ label: 'Whole history' }} />
				<Switch bind:checked={memory.includeNames} label="Include names" info="Puts author names in the history" top />
			</FieldRow>
		</FormSection>
		<FormSection title="Media" info="Limits and defaults for the images and video the bot takes in and makes">
			<FieldRow>
				<Slider bind:value={media.videoFrames} min={0} max={32} label="Video frames" info="Frames sampled from a video for the language model, none at zero" />
				<BytesField bind:value={() => Number(media.maxUploadBytes), (n) => (media.maxUploadBytes = BigInt(Math.round(n)))} label="Max upload" info="Largest attachment the bot downloads to look at" />
			</FieldRow>
			<FieldRow>
				<Select bind:value={() => media.imageSize || undefined, (v) => (media.imageSize = v ?? '')} options={imageSizes.map((s) => ({ value: s, label: s }))} label="Image size" info="Default image size as width x height" placeholder="1024x1024" clearable />
				<Slider bind:value={media.imageSteps} min={0} max={100} label="Image steps" info="Denoising steps, the model's default when empty" blank={media.imageSteps === 0} />
			</FieldRow>
			<FieldRow>
				<Slider bind:value={media.imageCount} min={1} max={4} label="Image count" info="Images made per request" />
				<Select bind:value={() => media.videoSize || undefined, (v) => (media.videoSize = v ?? '')} options={videoSizes.map((s) => ({ value: s, label: s }))} label="Video size" info="Default video size as width x height, the model's default when empty" placeholder="Model default" clearable />
			</FieldRow>
			<FieldRow>
				<Slider bind:value={media.videoSeconds} min={0} max={30} label="Video seconds" info="Default clip length, the model's default when empty" unit="s" blank={media.videoSeconds === 0} />
				<Select bind:value={() => media.videoFps || undefined, (v) => (media.videoFps = v ?? 0)} options={fpsOptions.map((f) => ({ value: f, label: String(f) }))} label="Video FPS" info="Default frames per second, the model's default when empty" placeholder="Model default" clearable />
			</FieldRow>
		</FormSection>
		<FormSection title="Presence" info="The status and activity lines shown on the bot's profile">
			<FieldRow>
				<Segmented bind:value={presence.status} options={presenceStatuses} label="Status" info="Online, idle, do not disturb or invisible" />
				<Select bind:value={() => presence.activityType || undefined, (v) => (presence.activityType = v ?? '')} options={activityTypes} label="Activity type" info="Playing, listening, watching, competing or custom" placeholder="Playing" clearable />
			</FieldRow>
			<FieldRow wide>
				<Chips bind:value={presence.activities} label="Activities" info="Status lines rotated in order" />
				<Duration bind:value={presence.rotateSeconds} base="s" max={86_400} label="Rotate" info="Time each activity line shows" emptyZero fallback={300} />
			</FieldRow>
		</FormSection>
		<FormSection title="Shards" info="Gateway connections the bot spreads its servers across">
			<FieldRow>
				<NumberField bind:value={spec.shardCount} min={0} label="Shard count" info="Gateway shards, Discord's recommendation when empty" placeholder={recommendedShards ? String(recommendedShards) : 'Recommended'} emptyZero />
				<Chips bind:value={() => spec.shardIds.map(String), (ids) => (spec.shardIds = ids.map(Number))} label="Shard ids" info="Shards this host runs, all of them when empty" numeric />
			</FieldRow>
		</FormSection>
	</div>
</div>

<style>
	.wrap {
		min-width: 0;
		container-type: inline-size;
	}
	.pairs {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: var(--nebu-space-6) var(--nebu-space-6);
		align-items: start;
	}
	@container (min-width: 1000px) {
		.pairs {
			grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
		}
	}
</style>
