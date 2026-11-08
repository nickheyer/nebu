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
	import Columns from '#lib/ui/layout/Columns.svelte';
	import ChannelTree from '#lib/pages/bots/ChannelTree.svelte';
	import { activityTypes, commandRoles, fpsOptions, guildOptions, imageSizes, presenceStatuses, videoSizes } from '#lib/pages/bots/bots.js';

	let { spec = $bindable(), guilds, recommendedShards }: { spec: BotSpec; guilds: BotGuild[]; recommendedShards: number } = $props();

	const engagement = $derived(spec.engagement!);
	const commands = $derived(spec.commands!);
	const memory = $derived(spec.memory!);
	const media = $derived(spec.media!);
	const presence = $derived(spec.presence!);
	const guildChoices = $derived(guildOptions(guilds));

	// Every role keeps a row, a blank name falling back to the role as the daemon does
	let names = $state<Record<string, string>>(untrack(() => Object.fromEntries(commandRoles.map((r) => [r, spec.commands?.names[r] || r]))));
	$effect(() => {
		const next = Object.fromEntries(commandRoles.map((r) => [r, names[r]?.trim() || r]));
		const stored = untrack(() => commands.names);
		if (commandRoles.some((r) => next[r] !== (stored[r] || r))) commands.names = next;
	});
</script>

<Columns ratio="1/1">
	{#snippet left()}
		<FormSection title="Engagement">
			<FieldRow>
				<Switch bind:checked={engagement.directMessages} label="Direct messages" />
				<Switch bind:checked={engagement.requireMention} label="Require mention" info="In guilds, answers only when mentioned, replied to, or called by a wake word" />
			</FieldRow>
			<FieldRow>
				<Switch bind:checked={engagement.answerBots} label="Answer bots" />
				<Switch bind:checked={engagement.memberEvents} label="Member events" info="Needs the privileged members intent" />
			</FieldRow>
			<Checklist bind:value={engagement.guildIds} options={guildChoices} label="Guilds" info="Empty allows every guild" empty="Start the bot to list guilds" />
			<FieldRow>
				<ChannelTree bind:value={engagement.channelIds} {guilds} label="Channels" info="Empty allows every channel" />
				<ChannelTree bind:value={engagement.deniedChannelIds} {guilds} label="Denied channels" />
			</FieldRow>
			<FieldRow>
				<Chips bind:value={engagement.userIds} label="Users" info="Empty allows everyone" numeric />
				<Chips bind:value={engagement.deniedUserIds} label="Denied users" numeric />
			</FieldRow>
			<FieldRow>
				<NumberField bind:value={engagement.maxBotChain} min={0} label="Max bot chain" placeholder="Default" info="Bot messages answered in a row, 3 when unset" emptyZero />
				<NumberField bind:value={engagement.maxConcurrent} min={0} label="Max concurrent" placeholder="Default" info="Generations at once, 4 when unset" emptyZero />
			</FieldRow>
			<FieldRow>
				<Duration bind:value={engagement.cooldownMs} base="ms" max={3_600_000} label="Cooldown" info="Between answers in one channel" />
				<Duration bind:value={engagement.userCooldownMs} base="ms" max={3_600_000} label="User cooldown" info="Between answers to one person" />
			</FieldRow>
		</FormSection>
		<FormSection title="Memory">
			<FieldRow>
				<NumberField bind:value={memory.messages} min={0} label="Messages" placeholder="Default" info="History messages read back, 20 when unset" emptyZero />
				<NumberField bind:value={memory.maxChars} min={0} label="Max characters" placeholder="Default" info="History characters kept, oldest dropped first, 8000 when unset" emptyZero />
			</FieldRow>
			<FieldRow>
				<Duration bind:value={() => memory.windowMinutes * 60, (s) => (memory.windowMinutes = Math.round(s / 60))} base="s" max={604_800} label="Window" info="Age of history kept, zero keeps all" />
				<Switch bind:checked={memory.includeNames} label="Include names" />
			</FieldRow>
		</FormSection>
		<FormSection title="Presence">
			<FieldRow>
				<Segmented bind:value={presence.status} options={presenceStatuses} label="Status" />
				<Select bind:value={presence.activityType} options={activityTypes} label="Activity type" clearable />
			</FieldRow>
			<FieldRow>
				<Chips bind:value={presence.activities} label="Activities" />
				<Duration bind:value={presence.rotateSeconds} base="s" max={86_400} label="Rotate" info="Zero is five minutes" />
			</FieldRow>
		</FormSection>
		<FormSection title="Shards">
			<FieldRow>
				<NumberField bind:value={spec.shardCount} min={0} label="Shard count" placeholder="Default" info={recommendedShards ? `Discord recommends ${recommendedShards} when unset` : 'Discord recommends a count when unset'} emptyZero />
				<Chips bind:value={() => spec.shardIds.map(String), (ids) => (spec.shardIds = ids.map(Number))} label="Shard ids" info="Empty runs every shard" numeric />
			</FieldRow>
		</FormSection>
	{/snippet}
	{#snippet right()}
		<FormSection title="Commands">
			<FieldRow>
				<Switch bind:checked={commands.enabled} label="Commands" info="Registers slash commands" />
				<Text bind:value={engagement.prefix} label="Prefix" info="Starts a text command" max={1} />
			</FieldRow>
			<FieldRow>
				<Checklist bind:value={commands.guildIds} options={guildChoices} label="Register in guilds" info="Empty registers globally" empty="Start the bot to list guilds" />
				<MultiSelect bind:value={commands.disabled} options={commandRoles.map((r) => ({ value: r, label: r }))} label="Disabled commands" />
			</FieldRow>
			<KeyValues bind:value={names} label="Command names" fixedKeys keyLabel="Role" valueLabel="Name" />
		</FormSection>
		<FormSection title="Media">
			<FieldRow>
				<Slider bind:value={media.videoFrames} min={0} max={32} label="Video frames" info="Sampled for the language model, zero off" />
				<BytesField bind:value={() => Number(media.maxUploadBytes), (n) => (media.maxUploadBytes = BigInt(Math.round(n)))} label="Max upload" info="Zero is ten million bytes" />
			</FieldRow>
			<FieldRow>
				<Select bind:value={media.imageSize} options={imageSizes.map((s) => ({ value: s, label: s }))} label="Image size" clearable />
				<Slider bind:value={media.imageSteps} min={0} max={100} label="Image steps" blank={media.imageSteps === 0} placeholder="Default" info="Unset leaves it to the image route" />
			</FieldRow>
			<FieldRow>
				<Slider bind:value={media.imageCount} min={1} max={4} label="Image count" />
				<Select bind:value={media.videoSize} options={videoSizes.map((s) => ({ value: s, label: s }))} label="Video size" clearable />
			</FieldRow>
			<FieldRow>
				<Slider bind:value={media.videoSeconds} min={0} max={30} label="Video seconds" unit="s" blank={media.videoSeconds === 0} placeholder="Default" info="Unset leaves it to the video route" />
				<Select bind:value={media.videoFps} options={fpsOptions.map((f) => ({ value: f, label: String(f) }))} label="Video FPS" clearable />
			</FieldRow>
		</FormSection>
	{/snippet}
</Columns>
