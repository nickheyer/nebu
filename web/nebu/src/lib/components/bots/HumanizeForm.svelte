<script lang="ts">
  import type { HumanizeFields } from '$lib/bots';
  import Field from '../ui/Field.svelte';
  import NumberInput from '../ui/NumberInput.svelte';
  import TextInput from '../ui/TextInput.svelte';
  import SwitchRow from '../ui/SwitchRow.svelte';
  import IdList from './IdList.svelte';

  let { fields = $bindable(), idPrefix }: { fields: HumanizeFields; idPrefix: string } = $props();

  const badDelay = $derived(fields.delayMinMs.trim() !== '' && fields.delayMaxMs.trim() !== '' && parseFloat(fields.delayMaxMs) < parseFloat(fields.delayMinMs));
  const badHours = $derived(fields.activeHours.trim() !== '' && !/^\d{1,2}:\d{2}-\d{1,2}:\d{2}$/.test(fields.activeHours.trim()));
  const needReactions = $derived(parseFloat(fields.reactionChance) > 0 && fields.reactions.length === 0);
</script>

<div class="flex flex-col gap-4">
  <SwitchRow bind:checked={fields.enabled} label="Humanize" description="Control reply timing and frequency." />
  {#if fields.enabled}
    <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2 lg:grid-cols-3">
      <Field label="Typing speed" for="{idPrefix}-cps" description="Controls the typing indicator.">
        <NumberInput id="{idPrefix}-cps" min={0} step={5} unit="chars/s" bind:value={fields.charsPerSecond} empty="45" />
      </Field>
      <Field label="Min delay" for="{idPrefix}-delay-min" description="Random delay before typing.">
        <NumberInput id="{idPrefix}-delay-min" integer min={0} step={100} unit="ms" bind:value={fields.delayMinMs} empty="0" invalid={badDelay} />
      </Field>
      <Field label="Max delay" for="{idPrefix}-delay-max" error={badDelay ? 'Below minimum delay' : undefined}>
        <NumberInput id="{idPrefix}-delay-max" integer min={0} step={100} unit="ms" bind:value={fields.delayMaxMs} empty="0" invalid={badDelay} />
      </Field>
      <Field label="Max typing time" for="{idPrefix}-max-typing">
        <NumberInput id="{idPrefix}-max-typing" integer min={0} step={1000} unit="ms" bind:value={fields.maxTypingMs} empty="12000" />
      </Field>
      <Field label="Characters per message" for="{idPrefix}-chunk" description="Up to 2000.">
        <NumberInput id="{idPrefix}-chunk" integer min={0} max={2000} step={100} unit="chars" bind:value={fields.maxChunkChars} empty="2000" disabled={!fields.splitMessages} />
      </Field>
      <Field label="Active hours" for="{idPrefix}-hours" description="Always active when empty." error={badHours ? 'Use HH:MM-HH:MM' : undefined}>
        <TextInput id="{idPrefix}-hours" mono bind:value={fields.activeHours} empty="09:00-22:30" invalid={badHours} />
      </Field>
      <Field label="Timezone" for="{idPrefix}-tz" description="IANA timezone. Uses the host timezone when empty.">
        <TextInput id="{idPrefix}-tz" mono bind:value={fields.timezone} empty="America/New_York" />
      </Field>
      <Field label="Reaction chance" for="{idPrefix}-reaction-chance" description="Probability, 0 to 1." error={needReactions ? 'Add reaction emojis' : undefined}>
        <NumberInput id="{idPrefix}-reaction-chance" min={0} max={1} step={0.05} bind:value={fields.reactionChance} empty="0" invalid={needReactions} />
      </Field>
      <Field label="Ambient reply chance" for="{idPrefix}-ambient" description="Probability of an unprompted reply, 0 to 1.">
        <NumberInput id="{idPrefix}-ambient" min={0} max={1} step={0.05} bind:value={fields.ambientReplyChance} empty="0" />
      </Field>
      <Field label="Ignore chance" for="{idPrefix}-ignore" description="Probability of skipping a reply, 0 to 1.">
        <NumberInput id="{idPrefix}-ignore" min={0} max={1} step={0.05} bind:value={fields.ignoreChance} empty="0" />
      </Field>
    </div>
    <Field label="Reactions" for="{idPrefix}-reactions" description="Chosen at random.">
      <IdList id="{idPrefix}-reactions" mono={false} bind:items={fields.reactions} empty="Emoji" />
    </Field>
    <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
      <SwitchRow bind:checked={fields.splitMessages} label="Split messages" description="Split at sentence and paragraph breaks." />
      <SwitchRow bind:checked={fields.casual} label="Casual" description="Lowercase text, no final period." />
      <SwitchRow bind:checked={fields.quoteReply} label="Reply to message" />
    </div>
  {/if}
</div>
