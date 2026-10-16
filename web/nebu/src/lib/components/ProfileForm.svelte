<script lang="ts">
  import { systemMessageItems, systemPromptItems, checksItems, extrasItems, profileError, type ProfileFields } from '$lib/gateway';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import TextArea from './ui/TextArea.svelte';
  import TextInput from './ui/TextInput.svelte';
  import ToolServerPicker from './ToolServerPicker.svelte';

  let { fields = $bindable(), idPrefix = 'profile' }: { fields: ProfileFields; idPrefix?: string } = $props();
  const error = $derived(profileError(fields));
</script>

<div class="flex flex-col gap-4">
  <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2 lg:grid-cols-3">
    <Field label="System messages after the first" for="{idPrefix}-system">
      <Select id="{idPrefix}-system" bind:value={fields.systemMessages} items={systemMessageItems} />
    </Field>
    <Field label="System prompt" for="{idPrefix}-system-prompt">
      <Select id="{idPrefix}-system-prompt" bind:value={fields.systemPrompt} items={systemPromptItems} />
    </Field>
    <Field label="Coding agent checks" for="{idPrefix}-checks">
      <Select id="{idPrefix}-checks" bind:value={fields.checks} items={checksItems} />
    </Field>
  </div>
  {#if fields.systemPrompt === 'replace'}
    <Field label="System text" for="{idPrefix}-system-text" error={error.startsWith('Replacing') ? error : ''} description="Sent to the runtime in place of the client's system prompt.">
      <TextArea id="{idPrefix}-system-text" bind:value={fields.systemText} empty="You are a terse coding assistant." />
    </Field>
  {/if}
  <div class="grid grid-cols-1 gap-x-5 gap-y-4 lg:grid-cols-2">
    <Field label="Client extras" for="{idPrefix}-extras" description="What the client sends beyond the model's needs: unmapped fields, its attribution block, and its identity headers.">
      <Select id="{idPrefix}-extras" bind:value={fields.extras} items={extrasItems} />
    </Field>
    <Field label="Overrides" for="{idPrefix}-overrides" error={error.startsWith('Overrides') ? error : ''} description="A JSON object written over every request the runtime gets, its fields winning.">
      <TextArea id="{idPrefix}-overrides" bind:value={fields.overrides} mono height="h-20" empty={'{"temperature": 0.6, "chat_template_kwargs": {"enable_thinking": false}}'} invalid={error.startsWith('Overrides')} />
    </Field>
  </div>
  <Field label="Hidden tools" for="{idPrefix}-hidden" description="Tools the runtime is never offered, by name, comma separated, * matching any run of characters.">
    <TextInput id="{idPrefix}-hidden" bind:value={fields.hiddenTools} mono empty="Artifact, Monitor, Cron*, mcp__plugin_*" />
  </Field>
  <Field label="Tool servers" for="{idPrefix}-tools" description="Every request on the route gets these tools, whatever client sends it.">
    <div id="{idPrefix}-tools"><ToolServerPicker bind:refs={fields.toolServers} bind:mutating={fields.mutatingTools} /></div>
  </Field>
</div>
