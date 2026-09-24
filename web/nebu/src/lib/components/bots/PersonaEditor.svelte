<script lang="ts">
  import type { PersonaFields } from '$lib/bots';
  import Field from '../ui/Field.svelte';
  import TextInput from '../ui/TextInput.svelte';
  import TextArea from '../ui/TextArea.svelte';
  import NumberInput from '../ui/NumberInput.svelte';
  import Disclosure from '../ui/Disclosure.svelte';
  import SwitchRow from '../ui/SwitchRow.svelte';
  import IdList from './IdList.svelte';
  import ModelSelect from './ModelSelect.svelte';
  import HumanizeForm from './HumanizeForm.svelte';
  import ChannelPicker from './ChannelPicker.svelte';

  let { persona = $bindable(), idPrefix, pickerBot = '' }: { persona: PersonaFields; idPrefix: string; pickerBot?: string } = $props();

  let samplingOpen = $state(false);
  let humanizeOpen = $state(persona.humanize.enabled);

  const badName = $derived(persona.name.trim() === '');
  const badTemperature = $derived(persona.sampling.temperature.trim() !== '' && (parseFloat(persona.sampling.temperature) < 0 || parseFloat(persona.sampling.temperature) > 2));
  const badTopP = $derived(persona.sampling.topP.trim() !== '' && (parseFloat(persona.sampling.topP) <= 0 || parseFloat(persona.sampling.topP) > 1));
  const samplingSummary = $derived.by(() => {
    const s = persona.sampling;
    const parts = [s.temperature && `temperature ${s.temperature}`, s.topP && `top_p ${s.topP}`, s.topK && `top_k ${s.topK}`, s.maxTokens && `${s.maxTokens} tokens`, s.seed && `seed ${s.seed}`, s.stop.length && `${s.stop.length} stop`].filter(Boolean);
    return parts.length ? parts.join(', ') : 'Runtime defaults';
  });
  const humanizeSummary = $derived(persona.humanize.enabled ? 'On' : 'Off');

  function addChannels(ids: string[]) {
    persona.channelIds = [...persona.channelIds, ...ids.filter((id) => !persona.channelIds.includes(id))];
  }
</script>

<div class="flex flex-col gap-5">
  <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
    <Field label="Name" for="{idPrefix}-name" required error={badName ? 'Name required' : undefined}>
      <TextInput id="{idPrefix}-name" bind:value={persona.name} empty="Ada" maxlength={80} />
    </Field>
    <Field label="Avatar URL" for="{idPrefix}-avatar" description="Used with webhooks.">
      <TextInput id="{idPrefix}-avatar" mono bind:value={persona.avatarUrl} empty="https://example.com/ada.png" />
    </Field>
  </div>

  <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
    <SwitchRow bind:checked={persona.webhook} label="Webhook" description="Use a custom name and avatar. Requires Manage Webhooks." />
    <SwitchRow bind:checked={persona.stream} label="Stream replies" description="Update messages during generation." />
    <SwitchRow bind:checked={persona.vision} label="Vision" description="Read images in messages." />
  </div>

  <Field label="System prompt" for="{idPrefix}-system">
    <TextArea id="{idPrefix}-system" height="h-48" bind:value={persona.systemPrompt} empty="You are Ada. Keep replies brief." />
  </Field>

  <div class="grid grid-cols-1 gap-x-5 gap-y-4 lg:grid-cols-3">
    <Field label="Language model" for="{idPrefix}-model">
      <ModelSelect id="{idPrefix}-model" kind="chat" bind:value={persona.model} />
    </Field>
    <Field label="Image model" for="{idPrefix}-image-model">
      <ModelSelect id="{idPrefix}-image-model" kind="image" bind:value={persona.imageModel} />
    </Field>
    <Field label="Video model" for="{idPrefix}-video-model">
      <ModelSelect id="{idPrefix}-video-model" kind="video" bind:value={persona.videoModel} />
    </Field>
  </div>

  <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
    <Field label="Image style" for="{idPrefix}-style" description="Appended to every image prompt.">
      <TextInput id="{idPrefix}-style" bind:value={persona.imageStyle} empty="watercolor, soft light" />
    </Field>
    <Field label="Negative prompt" for="{idPrefix}-negative" description="Applies to images and videos.">
      <TextInput id="{idPrefix}-negative" bind:value={persona.negativePrompt} empty="blurry, text, watermark" />
    </Field>
  </div>

  <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
    <Field label="Wake words" for="{idPrefix}-wake" description="Matches whole words, ignoring case.">
      <IdList id="{idPrefix}-wake" mono={false} bind:items={persona.wakeWords} empty="Wake word" />
    </Field>
    <Field label="Channels" for="{idPrefix}-channels" description="All allowed channels when empty.">
      <IdList id="{idPrefix}-channels" bind:items={persona.channelIds} empty="Channel ID">
        {#if pickerBot}<ChannelPicker botId={pickerBot} onPick={addChannels} />{/if}
      </IdList>
    </Field>
  </div>

  <Disclosure label="Sampling" summary={samplingSummary} bind:open={samplingOpen}>
    <div class="flex flex-col gap-4">
      <div class="grid grid-cols-2 gap-x-5 gap-y-4 lg:grid-cols-5">
        <Field label="Temperature" for="{idPrefix}-temperature" error={badTemperature ? 'Between 0 and 2' : undefined}>
          <NumberInput id="{idPrefix}-temperature" min={0} max={2} step={0.1} bind:value={persona.sampling.temperature} empty="runtime" invalid={badTemperature} />
        </Field>
        <Field label="Top p" for="{idPrefix}-top-p" error={badTopP ? 'Above 0, at most 1' : undefined}>
          <NumberInput id="{idPrefix}-top-p" min={0} max={1} step={0.05} bind:value={persona.sampling.topP} empty="runtime" invalid={badTopP} />
        </Field>
        <Field label="Top k" for="{idPrefix}-top-k">
          <NumberInput id="{idPrefix}-top-k" integer min={0} bind:value={persona.sampling.topK} empty="runtime" />
        </Field>
        <Field label="Max tokens" for="{idPrefix}-max-tokens">
          <NumberInput id="{idPrefix}-max-tokens" integer min={0} step={64} bind:value={persona.sampling.maxTokens} empty="runtime" />
        </Field>
        <Field label="Seed" for="{idPrefix}-seed">
          <NumberInput id="{idPrefix}-seed" integer min={0} bind:value={persona.sampling.seed} empty="random" />
        </Field>
      </div>
      <Field label="Stop sequences" for="{idPrefix}-stop" description="Stop generation at these strings.">
        <IdList id="{idPrefix}-stop" bind:items={persona.sampling.stop} empty="Stop sequence" />
      </Field>
    </div>
  </Disclosure>

  <Disclosure label="Humanize" summary={humanizeSummary} bind:open={humanizeOpen}>
    <HumanizeForm bind:fields={persona.humanize} idPrefix="{idPrefix}-humanize" />
  </Disclosure>
</div>
