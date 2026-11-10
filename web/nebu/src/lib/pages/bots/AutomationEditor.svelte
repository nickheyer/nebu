<script lang="ts">
	import { ActionKind, TriggerKind, type Automation, type BotGuild, type Persona } from '#lib/proto/nebu/v1/bot_pb.js';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Cron from '#lib/ui/fields/Cron.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import Percent from '#lib/ui/fields/Percent.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Textarea from '#lib/ui/fields/Textarea.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import ChannelTree from '#lib/pages/bots/ChannelTree.svelte';
	import { actionKinds, guildOptions, routeFetch, templateHelp, triggerKinds } from '#lib/pages/bots/bots.js';

	let { automation = $bindable(), personas, guilds }: { automation: Automation; personas: Persona[]; guilds: BotGuild[] } = $props();

	const trigger = $derived(automation.trigger!);
	const action = $derived(automation.action!);
	// Actions that generate through a model
	const generates = $derived(action.kind === ActionKind.CHAT || action.kind === ActionKind.IMAGE || action.kind === ActionKind.VIDEO);
	const routes = routeFetch();
	// The persona that acts, the first one when none is named
	const acting = $derived(personas.find((p) => p.id === automation.personaId) ?? personas[0]);
</script>

{#snippet actionAdvanced()}
	<FieldRow>
		<Picker bind:value={() => action.model || undefined, (v) => (action.model = v ?? '')} fetch={routes} label="Model" placeholder={acting?.model || undefined} clearable current={action.model || undefined} />
		<Switch bind:checked={action.reply} label="Reply to the message" top />
	</FieldRow>
{/snippet}

<Stack gap={24}>
	<FormSection>
		<FieldRow>
			<Text bind:value={automation.name} label="Name" max={64} required />
			<Switch bind:checked={automation.enabled} label="Enabled" top />
		</FieldRow>
	</FormSection>
	<FormSection title="When">
		<FieldRow>
			<Select bind:value={trigger.kind} options={triggerKinds} label="Trigger" />
			{#if trigger.kind === TriggerKind.SCHEDULE}
				<Cron bind:value={trigger.cron} />
			{:else if trigger.kind === TriggerKind.KEYWORD}
				<Text bind:value={trigger.pattern} label="Pattern" info="Regular expression" mono max={256} />
			{:else if trigger.kind === TriggerKind.REACTION}
				<Text bind:value={trigger.pattern} label="Emoji" max={8} />
			{:else if trigger.kind === TriggerKind.COMMAND}
				<Text bind:value={trigger.command} label="Command" info="The word after the prefix" max={32} />
			{/if}
		</FieldRow>
		{#if trigger.kind === TriggerKind.SCHEDULE && !trigger.cron}
			<Duration bind:value={trigger.everySeconds} base="s" max={2_592_000} label="Every" emptyZero />
		{/if}
		<FieldRow wide>
			<Percent value={(automation.chance || 1) * 100} max={100} label="Chance" info="Share of matches that fire" onchange={(n: number) => (automation.chance = n >= 100 ? 0 : n / 100)} />
			<Duration bind:value={automation.cooldownMs} base="ms" max={86_400_000} label="Pause between runs" emptyZero />
		</FieldRow>
	</FormSection>
	<FormSection title="Do" advanced={generates ? actionAdvanced : undefined}>
		<FieldRow>
			<Select bind:value={action.kind} options={actionKinds} label="Action" />
			{#if action.kind === ActionKind.REACT}
				<Text bind:value={action.emoji} label="Emoji" max={8} />
			{:else if action.kind === ActionKind.TEXT}
				<Switch bind:checked={action.reply} label="Reply to the message" top />
			{/if}
		</FieldRow>
		{#if action.kind !== ActionKind.REACT}
			<Textarea bind:value={action.template} label={generates ? 'Prompt' : 'Message'} message={templateHelp} rows={3} mono />
		{/if}
	</FormSection>
	<FormSection title="Where">
		{#if personas.length > 1}
			<Select bind:value={() => automation.personaId || undefined, (v) => (automation.personaId = v ?? '')} options={personas.map((p) => ({ value: p.id, label: p.name }))} label="Persona" clearable placeholder={personas[0]?.name} />
		{/if}
		{#if guilds.length}
			<FieldRow>
				<Checklist bind:value={automation.guildIds} options={guildOptions(guilds)} label="Servers" info="Every server when none is checked" />
				<ChannelTree bind:value={automation.channelIds} {guilds} label="Channels" info="Every channel when none is checked" />
			</FieldRow>
		{/if}
	</FormSection>
</Stack>
