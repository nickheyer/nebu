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
		<FormSection title="Answering">
			<FieldRow>
				<Switch bind:checked={engagement.directMessages} label="Direct messages" />
				<Switch bind:checked={engagement.requireMention} label="Only when mentioned" info="In servers, a mention, a reply or a wake word" />
			</FieldRow>
			<FieldRow>
				<Switch bind:checked={engagement.answerBots} label="Answer other bots" />
				<Switch bind:checked={engagement.memberEvents} label="See joins and leaves" info="Needs the members intent" />
			</FieldRow>
			{#if guilds.length}
				<Checklist bind:value={engagement.guildIds} options={guildChoices} label="Servers" info="Every server when none is checked" />
				<FieldRow>
					<ChannelTree bind:value={engagement.channelIds} {guilds} label="Channels" info="Every channel when none is checked" />
					<ChannelTree bind:value={engagement.deniedChannelIds} {guilds} label="Blocked channels" />
				</FieldRow>
			{/if}
			<FieldRow>
				<Chips bind:value={engagement.userIds} label="Users" info="Discord user ids, everyone when empty" numeric />
				<Chips bind:value={engagement.deniedUserIds} label="Blocked users" numeric />
			</FieldRow>
			<FieldRow>
				<NumberField bind:value={engagement.maxBotChain} min={0} label="Bot messages in a row" info="Stops answering after this many" fallback={3} emptyZero />
				<NumberField bind:value={engagement.maxConcurrent} min={0} label="Answers at once" fallback={4} emptyZero />
			</FieldRow>
			<FieldRow wide>
				<Duration bind:value={engagement.cooldownMs} base="ms" smallest="s" max={3_600_000} label="Pause per channel" emptyZero />
				<Duration bind:value={engagement.userCooldownMs} base="ms" smallest="s" max={3_600_000} label="Pause per person" emptyZero />
			</FieldRow>
		</FormSection>
		<FormSection title="Commands">
			<FieldRow>
				<Switch bind:checked={commands.enabled} label="Slash commands" top />
				<Text bind:value={engagement.prefix} label="Text prefix" info="Starts a text command, such as !" max={1} />
			</FieldRow>
			{#if guilds.length}
				<Checklist bind:value={commands.guildIds} options={guildChoices} label="Register in" info="At once in the checked servers, up to an hour everywhere when none is" />
			{/if}
			<MultiSelect bind:value={commands.disabled} options={commandRoles.map((r) => ({ value: r, label: r }))} label="Turned off" />
			<KeyValues bind:value={names} label="Names" fixedKeys keyLabel="Command" valueLabel="Name" />
		</FormSection>
		<FormSection title="Memory">
			<FieldRow>
				<NumberField bind:value={memory.messages} min={0} label="Messages" fallback={20} emptyZero />
				<NumberField bind:value={memory.maxChars} min={0} label="Characters" fallback={8000} emptyZero />
			</FieldRow>
			<FieldRow wide>
				<Duration bind:value={() => memory.windowMinutes * 60, (s) => (memory.windowMinutes = Math.round(s / 60))} base="s" max={604_800} label="No older than" emptyZero />
				<Switch bind:checked={memory.includeNames} label="Author names" top />
			</FieldRow>
		</FormSection>
		<FormSection title="Media">
			<FieldRow>
				<NumberField bind:value={media.videoFrames} min={0} max={32} label="Video frames" info="Frames of a video shown to the model" />
				<BytesField bind:value={() => Number(media.maxUploadBytes), (n) => (media.maxUploadBytes = BigInt(Math.round(n)))} label="Largest attachment" />
			</FieldRow>
			<FieldRow>
				<Select bind:value={() => media.imageSize || undefined, (v) => (media.imageSize = v ?? '')} options={imageSizes.map((s) => ({ value: s, label: s }))} label="Image size" placeholder="1024x1024" clearable />
				<NumberField bind:value={media.imageSteps} min={0} max={100} label="Image steps" emptyZero />
			</FieldRow>
			<FieldRow>
				<NumberField bind:value={media.imageCount} min={1} max={4} label="Images" />
				<Select bind:value={() => media.videoSize || undefined, (v) => (media.videoSize = v ?? '')} options={videoSizes.map((s) => ({ value: s, label: s }))} label="Video size" clearable />
			</FieldRow>
			<FieldRow>
				<NumberField bind:value={media.videoSeconds} min={0} max={30} label="Video length" unit="s" emptyZero />
				<Select bind:value={() => media.videoFps || undefined, (v) => (media.videoFps = v ?? 0)} options={fpsOptions.map((f) => ({ value: f, label: String(f) }))} label="Video FPS" clearable />
			</FieldRow>
		</FormSection>
		<FormSection title="Presence">
			<FieldRow>
				<Segmented bind:value={presence.status} options={presenceStatuses} label="Status" />
				<Select bind:value={() => presence.activityType || undefined, (v) => (presence.activityType = v ?? '')} options={activityTypes} label="Activity" placeholder="Playing" clearable />
			</FieldRow>
			<FieldRow wide>
				<Chips bind:value={presence.activities} label="Activity lines" />
				<Duration bind:value={presence.rotateSeconds} base="s" max={86_400} label="Change every" emptyZero fallback={300} />
			</FieldRow>
		</FormSection>
		<FormSection title="Shards">
			<FieldRow>
				<NumberField bind:value={spec.shardCount} min={0} label="Count" fallback={recommendedShards || undefined} emptyZero />
				<Chips bind:value={() => spec.shardIds.map(String), (ids) => (spec.shardIds = ids.map(Number))} label="Run only" info="Shard numbers this host runs, all when empty" numeric />
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
