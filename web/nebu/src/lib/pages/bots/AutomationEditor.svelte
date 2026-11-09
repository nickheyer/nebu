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
</script>

{#snippet triggerAdvanced()}
	<Percent value={automation.chance * 100} label="Chance" info="Share of matches that fire, every match at zero" onchange={(n: number) => (automation.chance = n / 100)} />
{/snippet}

{#snippet actionAdvanced()}
	<FieldRow>
		<Picker bind:value={() => action.model || undefined, (v) => (action.model = v ?? '')} fetch={routes} label="Model" info="Route the action answers through, the persona's own when empty" placeholder="Persona's model" clearable current={action.model || undefined} />
		<Switch bind:checked={action.reply} label="Reply" info="Posts as a reply to the triggering message" top />
	</FieldRow>
{/snippet}

{#snippet scopeAdvanced()}
	<Duration bind:value={automation.cooldownMs} base="ms" max={86_400_000} label="Cooldown" info="Least time between two firings" />
{/snippet}

<Stack gap={24}>
	<FormSection title="Trigger" info="A chance above zero fires the automation on only that share of matches" advanced={triggerAdvanced}>
		<FieldRow>
			<Text bind:value={automation.name} label="Name" info="Shown in the automations list" max={64} required />
			<Switch bind:checked={automation.enabled} label="Enabled" info="Whether the trigger is armed" top />
		</FieldRow>
		<FieldRow>
			<Select bind:value={trigger.kind} options={triggerKinds} label="Kind" info="Schedule, keyword, reaction or command" />
			{#if trigger.kind === TriggerKind.SCHEDULE}
				<Cron bind:value={trigger.cron} info="When the automation runs" />
			{:else if trigger.kind === TriggerKind.KEYWORD}
				<Text bind:value={trigger.pattern} label="Pattern" info="A regular expression the message must match" placeholder="Regex" mono max={256} />
			{:else if trigger.kind === TriggerKind.REACTION}
				<Text bind:value={trigger.pattern} label="Emoji" info="The reaction that fires it" max={8} />
			{:else if trigger.kind === TriggerKind.COMMAND}
				<Text bind:value={trigger.command} label="Command" info="The word after the prefix" max={32} />
			{/if}
		</FieldRow>
		{#if trigger.kind === TriggerKind.SCHEDULE}
			<Duration bind:value={trigger.everySeconds} base="s" max={2_592_000} label="Every" info="Time between runs when there is no cron" placeholder="Cron alone" emptyZero />
		{/if}
	</FormSection>
	<FormSection title="Action" info="What the bot does when the trigger fires" advanced={generates ? actionAdvanced : undefined}>
		<FieldRow>
			<Select bind:value={action.kind} options={actionKinds} label="Kind" info="Text posted as written, a generated answer or a reaction" />
			{#if action.kind === ActionKind.REACT}
				<Text bind:value={action.emoji} label="Emoji" info="The reaction to add" max={8} />
			{:else if action.kind === ActionKind.TEXT}
				<Switch bind:checked={action.reply} label="Reply" info="Posts as a reply to the triggering message" top />
			{/if}
		</FieldRow>
		{#if action.kind !== ActionKind.REACT}
			<Textarea bind:value={action.template} label={generates ? 'Prompt template' : 'Template'} info={generates ? 'What the model is asked, with the placeholders below filled in from the triggering message' : 'What gets posted, with the placeholders below filled in from the triggering message'} message={templateHelp} rows={3} mono />
		{/if}
	</FormSection>
	<FormSection title="Scope" info="Who acts and where the automation listens" advanced={scopeAdvanced}>
		<Select bind:value={() => automation.personaId || undefined, (v) => (automation.personaId = v ?? '')} options={personas.map((p) => ({ value: p.id, label: p.name }))} label="Persona" info="Who acts, the first persona when empty" clearable placeholder={personas[0] ? `Default (${personas[0].name})` : 'No personas'} />
		<FieldRow>
			<Checklist bind:value={automation.guildIds} options={guildOptions(guilds)} label="Guilds" info="Servers the automation acts in, every allowed one when empty" empty={guilds.length ? undefined : 'Not connected'} />
			<ChannelTree bind:value={automation.channelIds} {guilds} label="Channels" info="Channels for scheduled posts and message triggers, every allowed one when empty" />
		</FieldRow>
	</FormSection>
</Stack>
