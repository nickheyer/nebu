<script lang="ts">
  import { ActionKind, TriggerKind } from '$proto/bot_pb';
  import { actionKinds, modelKindOf, templateFields, triggerKinds, type AutomationFields, type PersonaFields } from '$lib/bots';
  import Field from '../ui/Field.svelte';
  import TextInput from '../ui/TextInput.svelte';
  import TextArea from '../ui/TextArea.svelte';
  import NumberInput from '../ui/NumberInput.svelte';
  import Select from '../ui/Select.svelte';
  import SwitchRow from '../ui/SwitchRow.svelte';
  import IdList from './IdList.svelte';
  import ModelSelect from './ModelSelect.svelte';
  import ChannelPicker from './ChannelPicker.svelte';

  let { automation = $bindable(), personas, idPrefix, pickerBot = '' }: { automation: AutomationFields; personas: PersonaFields[]; idPrefix: string; pickerBot?: string } = $props();

  const trigger = $derived(Number(automation.triggerKind) as TriggerKind);
  const action = $derived(Number(automation.actionKind) as ActionKind);
  const schedule = $derived(trigger === TriggerKind.SCHEDULE);
  const modelKind = $derived(modelKindOf(action));
  const templated = $derived(action !== ActionKind.REACT);
  const personaItems = $derived([{ value: '', label: personas[0]?.name ? `${personas[0].name} (default)` : 'Default persona' }, ...personas.map((p) => ({ value: p.id, label: p.name || 'Unnamed persona' }))]);
  const badSchedule = $derived(schedule && automation.cron.trim() === '' && !(parseFloat(automation.everySeconds) > 0));
  const badScheduleChannels = $derived(schedule && automation.channelIds.length === 0 && action !== ActionKind.PRESENCE);
  const badChance = $derived(automation.chance.trim() !== '' && (parseFloat(automation.chance) < 0 || parseFloat(automation.chance) > 1));

  function addChannels(ids: string[]) {
    automation.channelIds = [...automation.channelIds, ...ids.filter((id) => !automation.channelIds.includes(id))];
  }
  function addGuilds(ids: string[]) {
    automation.guildIds = [...automation.guildIds, ...ids.filter((id) => !automation.guildIds.includes(id))];
  }
</script>

<div class="flex flex-col gap-5">
  <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
    <Field label="Name" for="{idPrefix}-name">
      <TextInput id="{idPrefix}-name" bind:value={automation.name} empty="Morning greeting" />
    </Field>
    <Field label="Persona" for="{idPrefix}-persona">
      <Select id="{idPrefix}-persona" bind:value={automation.personaId} items={personaItems} />
    </Field>
  </div>

  <div class="flex flex-col gap-4">
    <div class="caps text-fg-faint">Trigger</div>
    <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
      <Field label="Kind" for="{idPrefix}-trigger">
        <Select id="{idPrefix}-trigger" bind:value={automation.triggerKind} items={triggerKinds} />
      </Field>
      {#if schedule}
        <Field label="Cron" for="{idPrefix}-cron" description="minute hour day month weekday" error={badSchedule ? 'Set a cron expression or interval' : undefined}>
          <TextInput id="{idPrefix}-cron" mono bind:value={automation.cron} empty="0 9 * * mon-fri" invalid={badSchedule} />
        </Field>
        <Field label="Interval" for="{idPrefix}-every" description="Used when cron is empty.">
          <NumberInput id="{idPrefix}-every" integer min={0} step={60} unit="s" bind:value={automation.everySeconds} empty="off" invalid={badSchedule} />
        </Field>
      {:else if trigger === TriggerKind.KEYWORD}
        <Field label="Pattern" for="{idPrefix}-pattern" description="Regex, ignoring case. {'{{index .Match 1}}'} is the first capture group." required>
          <TextInput id="{idPrefix}-pattern" mono bind:value={automation.pattern} empty="\bgood (morning|night)\b" />
        </Field>
      {:else if trigger === TriggerKind.REACTION}
        <Field label="Reaction emoji" for="{idPrefix}-pattern" required>
          <TextInput id="{idPrefix}-pattern" bind:value={automation.pattern} empty="👀" />
        </Field>
      {:else if trigger === TriggerKind.COMMAND}
        <Field label="Command" for="{idPrefix}-command" description="Without the command prefix." required>
          <TextInput id="{idPrefix}-command" mono bind:value={automation.command} empty="roll" />
        </Field>
      {:else if trigger === TriggerKind.MEMBER_JOIN}
        <div class="text-xs leading-5 text-fg-muted sm:pt-6">Enable Member events and Discord's Server Members intent.</div>
      {/if}
    </div>
    <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
      <Field label="Channels" for="{idPrefix}-channels" description={schedule ? 'Where to post. Optional for presence.' : 'All allowed channels when empty.'} required={schedule && action !== ActionKind.PRESENCE} error={badScheduleChannels ? 'Select a channel' : undefined}>
        <IdList id="{idPrefix}-channels" bind:items={automation.channelIds} empty="Channel ID">
          {#if pickerBot}<ChannelPicker botId={pickerBot} onPick={addChannels} />{/if}
        </IdList>
      </Field>
      <Field label="Guilds" for="{idPrefix}-guilds" description="All guilds when empty.">
        <IdList id="{idPrefix}-guilds" bind:items={automation.guildIds} empty="Guild ID">
          {#if pickerBot}<ChannelPicker botId={pickerBot} guilds onPick={addGuilds} />{/if}
        </IdList>
      </Field>
      <Field label="Chance" for="{idPrefix}-chance" description="Trigger probability, 0 to 1." error={badChance ? 'Between 0 and 1' : undefined}>
        <NumberInput id="{idPrefix}-chance" min={0} max={1} step={0.05} bind:value={automation.chance} empty="always" invalid={badChance} />
      </Field>
      <Field label="Cooldown" for="{idPrefix}-cooldown" description="Minimum time between runs.">
        <NumberInput id="{idPrefix}-cooldown" integer min={0} step={1000} unit="ms" bind:value={automation.cooldownMs} empty="0" />
      </Field>
    </div>
  </div>

  <div class="flex flex-col gap-4">
    <div class="caps text-fg-faint">Action</div>
    <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
      <Field label="Kind" for="{idPrefix}-action">
        <Select id="{idPrefix}-action" bind:value={automation.actionKind} items={actionKinds} />
      </Field>
      {#if action === ActionKind.REACT}
        <Field label="Emoji" for="{idPrefix}-emoji" description="Added to the triggering message." required>
          <TextInput id="{idPrefix}-emoji" bind:value={automation.emoji} empty="🎉" />
        </Field>
      {:else if modelKind}
        <Field label="Model" for="{idPrefix}-model" description="Overrides the persona's model.">
          <ModelSelect id="{idPrefix}-model" kind={modelKind} bind:value={automation.model} />
        </Field>
      {/if}
    </div>
    {#if templated}
      <Field label={action === ActionKind.TEXT ? 'Message template' : action === ActionKind.PRESENCE ? 'Activity template' : 'Prompt template'} for="{idPrefix}-template" required>
        <TextArea id="{idPrefix}-template" mono bind:value={automation.template} empty={'Hello {{.Channel}}'} />
        {#snippet sub()}
          <span class="text-xs text-fg-faint">Fields: {#each templateFields as f, i (f)}{#if i}{' '}{/if}<code class="font-mono text-fg-muted">{f}</code>{/each}</span>
        {/snippet}
      </Field>
    {/if}
    {#if action !== ActionKind.PRESENCE && action !== ActionKind.REACT}
      <SwitchRow bind:checked={automation.reply} label="Reply to message" disabled={schedule} />
    {/if}
  </div>
</div>
