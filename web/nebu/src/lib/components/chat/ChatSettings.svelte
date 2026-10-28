<script lang="ts">
  import type { Session } from '$lib/chats';
  import type { Capabilities } from '$lib/generate';
  import Field from '$lib/components/ui/Field.svelte';
  import Segmented from '$lib/components/ui/Segmented.svelte';
  import SwitchRow from '$lib/components/ui/SwitchRow.svelte';
  import { theater, setTheater } from '$lib/theater.svelte';
  import NumberInput from '$lib/components/ui/NumberInput.svelte';
  import TextInput from '$lib/components/ui/TextInput.svelte';
  import TextArea from '$lib/components/ui/TextArea.svelte';
  import ToolServerPicker from '$lib/components/ToolServerPicker.svelte';
  import MediaSettings, { type MediaForm } from './MediaSettings.svelte';

  let {
    session = $bindable(),
    media,
    form = $bindable(),
    mode,
    caps,
    hasInit,
    toolsProblem,
    needsKey,
    key = $bindable(''),
    onkey,
    gatewayBase,
    modelName = ''
  }: {
    session: Session;
    media: boolean;
    form: MediaForm;
    mode: 'image' | 'video';
    caps: Capabilities | null;
    hasInit: boolean;
    // Why the custom tools JSON does not parse, empty when it does
    toolsProblem: string;
    // Whether the gateway wants a key and no user is signed in
    needsKey: boolean;
    key?: string;
    onkey: () => void;
    gatewayBase: string;
    // Shown as the placeholder for the character's name
    modelName?: string;
  } = $props();
</script>

<div class="flex flex-col gap-5">
  <SwitchRow checked={theater.on} onchange={setTheater} label="Theater mode" description="Hide everything but the chat. Esc leaves it. Remembered by this browser." />
  {#if media}
    <MediaSettings bind:form {mode} {caps} {hasInit} />
  {:else}
    <Field label="Wire format">
      <Segmented bind:value={session.dialect} tabs={[{ id: 'openai', label: 'OpenAI' }, { id: 'anthropic', label: 'Anthropic' }, { id: 'ollama', label: 'Ollama' }]} />
    </Field>
    <SwitchRow bind:checked={session.stream} label="Stream" />
    <Field label="Scene" for="chat-system" description="System prompt. Who the character is, where this happens, how it should read.">
      <TextArea id="chat-system" height="h-40" bind:value={session.system} empty="You are Mara, a weary lighthouse keeper on a storm-wracked coast. Stay in character. Write actions in *asterisks*." />
    </Field>
    <div class="grid grid-cols-2 gap-3">
      <Field label="You" for="chat-user-name"><TextInput id="chat-user-name" bind:value={session.userName} empty="You" maxlength={40} /></Field>
      <Field label="Character" for="chat-assistant-name"><TextInput id="chat-assistant-name" bind:value={session.assistantName} empty={modelName || 'Assistant'} maxlength={40} /></Field>
    </div>
    <div class="grid grid-cols-2 gap-3 @lg:grid-cols-3">
      <Field label="Temperature" for="chat-temp"><NumberInput id="chat-temp" min={0} max={2} step={0.1} bind:value={session.temperature} /></Field>
      <Field label="Top P" for="chat-topp"><NumberInput id="chat-topp" min={0} max={1} step={0.05} bind:value={session.topP} /></Field>
      <Field label="Top K" for="chat-topk"><NumberInput id="chat-topk" integer min={0} bind:value={session.topK} /></Field>
      <Field label="Max tokens" for="chat-max"><NumberInput id="chat-max" integer min={1} step={64} bind:value={session.maxTokens} /></Field>
      <Field label="Seed" for="chat-seed"><NumberInput id="chat-seed" integer min={0} bind:value={session.seed} /></Field>
      <Field label="Stop sequences" for="chat-stop"><TextInput id="chat-stop" mono bind:value={session.stop} empty="###, User:" /></Field>
    </div>
    <Field label="Tool servers" for="chat-tool-servers" description="Run by the gateway for the model.">
      <div id="chat-tool-servers"><ToolServerPicker bind:refs={session.toolServers} bind:mutating={session.mutatingTools} /></div>
    </Field>
    {#if session.toolServers.length && session.mutatingTools}
      <SwitchRow bind:checked={session.approveTools} label="Approve mutating tools" description="Calls that change something wait for Run or Decline." />
    {/if}
    <Field label="Custom tools (JSON)" for="chat-tools" description="Functions answered by hand on this page." error={toolsProblem || undefined}>
      <TextArea id="chat-tools" mono height="h-36" bind:value={session.tools} empty={'[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]'} invalid={!!toolsProblem} />
    </Field>
  {/if}
  {#if needsKey}
    <Field label="API key" for="chat-key">
      <TextInput id="chat-key" mono type="password" bind:value={key} onchange={onkey} />
    </Field>
  {/if}
  <p class="truncate font-mono text-xs text-fg-faint" title={gatewayBase}>{gatewayBase}</p>
</div>
