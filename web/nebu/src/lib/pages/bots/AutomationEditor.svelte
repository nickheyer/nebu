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
	import { actionKinds, guildOptions, routeFetch, templateVariables, triggerKinds } from '#lib/pages/bots/bots.js';

	let { automation = $bindable(), personas, guilds }: { automation: Automation; personas: Persona[]; guilds: BotGuild[] } = $props();

	const trigger = $derived(automation.trigger!);
	const action = $derived(automation.action!);
	// Actions that generate through a model and so take a route
	const generates = $derived(action.kind === ActionKind.CHAT || action.kind === ActionKind.IMAGE || action.kind === ActionKind.VIDEO);
	const routes = routeFetch();
	const templateInfo = `Go template over ${templateVariables}`;
</script>

{#snippet triggerAdvanced()}
	<Percent value={automation.chance * 100} label="Chance" info="0 is always" onchange={(n: number) => (automation.chance = n / 100)} />
{/snippet}

{#snippet actionAdvanced()}
	<FieldRow>
		<Picker bind:value={() => action.model || undefined, (v) => (action.model = v ?? '')} fetch={routes} label="Model" placeholder="The persona's" clearable current={action.model || undefined} />
		<Switch bind:checked={action.reply} label="Reply" info="Replies to the triggering message" />
	</FieldRow>
{/snippet}

{#snippet scopeAdvanced()}
	<Duration bind:value={automation.cooldownMs} base="ms" max={86_400_000} label="Cooldown" />
{/snippet}

<Stack gap={24}>
	<FormSection title="Trigger" advanced={triggerAdvanced}>
		<FieldRow>
			<Text bind:value={automation.name} label="Name" max={64} required />
			<Switch bind:checked={automation.enabled} label="Enabled" />
		</FieldRow>
		<FieldRow>
			<Select bind:value={trigger.kind} options={triggerKinds} label="Kind" />
			{#if trigger.kind === TriggerKind.SCHEDULE}
				<Cron bind:value={trigger.cron} />
			{:else if trigger.kind === TriggerKind.KEYWORD}
				<Text bind:value={trigger.pattern} label="Pattern" info="Regular expression" mono max={256} />
			{:else if trigger.kind === TriggerKind.REACTION}
				<Text bind:value={trigger.pattern} label="Emoji" max={8} />
			{:else if trigger.kind === TriggerKind.COMMAND}
				<Text bind:value={trigger.command} label="Command" info="Word after the prefix" max={32} />
			{/if}
		</FieldRow>
		{#if trigger.kind === TriggerKind.SCHEDULE}
			<Duration bind:value={trigger.everySeconds} base="s" max={2_592_000} label="Every" info="Interval without a cron" />
		{/if}
	</FormSection>
	<FormSection title="Action" advanced={generates ? actionAdvanced : undefined}>
		<FieldRow>
			<Select bind:value={action.kind} options={actionKinds} label="Kind" />
			{#if action.kind === ActionKind.REACT}
				<Text bind:value={action.emoji} label="Emoji" max={8} />
			{:else if action.kind === ActionKind.TEXT}
				<Switch bind:checked={action.reply} label="Reply" info="Replies to the triggering message" />
			{/if}
		</FieldRow>
		{#if action.kind !== ActionKind.REACT}
			<Textarea bind:value={action.template} label={generates ? 'Prompt template' : 'Template'} info={templateInfo} rows={3} mono />
		{/if}
	</FormSection>
	<FormSection title="Scope" advanced={scopeAdvanced}>
		<Select bind:value={() => automation.personaId || undefined, (v) => (automation.personaId = v ?? '')} options={personas.map((p) => ({ value: p.id, label: p.name }))} label="Persona" clearable placeholder="First persona" />
		<FieldRow>
			<Checklist bind:value={automation.guildIds} options={guildOptions(guilds)} label="Guilds" info="Empty is every guild" empty="Not connected" />
			<ChannelTree bind:value={automation.channelIds} {guilds} label="Channels" info="Empty is every allowed channel" />
		</FieldRow>
	</FormSection>
</Stack>
