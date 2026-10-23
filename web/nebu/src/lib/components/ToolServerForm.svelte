<script lang="ts">
  import { ToolServerKind } from '$proto/settings_pb';
  import { kindItems, providerItems, type ToolServerFields } from '$lib/tools';
  import { KeyRound, Eye, EyeOff } from '@lucide/svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import TextInput from './ui/TextInput.svelte';
  import TextArea from './ui/TextArea.svelte';
  import SwitchRow from './ui/SwitchRow.svelte';
  import IdList from './bots/IdList.svelte';

  let { fields = $bindable(), idPrefix = 'tool', problem = '' }: { fields: ToolServerFields; idPrefix?: string; problem?: string } = $props();

  let showKey = $state(false);
  const kind = $derived(Number(fields.kind) as ToolServerKind);
</script>

<div class="flex flex-col gap-4">
  <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
    <Field label="Name" for="{idPrefix}-name" required description="Routes, personas, and requests attach it by this name." error={problem.includes('ame') ? problem : undefined}>
      <TextInput id="{idPrefix}-name" mono bind:value={fields.name} empty="web" maxlength={32} />
    </Field>
    <Field label="Kind" for="{idPrefix}-kind">
      <Select id="{idPrefix}-kind" bind:value={fields.kind} items={kindItems} />
    </Field>
  </div>
  {#if kind === ToolServerKind.SEARCH}
    <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
      <Field label="Provider" for="{idPrefix}-provider">
        <Select id="{idPrefix}-provider" bind:value={fields.provider} items={providerItems} />
      </Field>
      {#if fields.provider === 'searxng'}
        <Field label="SearXNG URL" for="{idPrefix}-searxng" description="The instance needs json in search.formats." error={problem.includes('SearXNG') ? problem : undefined}>
          <TextInput id="{idPrefix}-searxng" mono bind:value={fields.searxngUrl} empty="https://searx.example" />
        </Field>
      {:else}
        <Field label="Brave token" for="{idPrefix}-brave" description="From the Brave Search API dashboard." error={problem.includes('Brave') ? problem : undefined}>
          <div class="relative">
            <TextInput id="{idPrefix}-brave" mono type={showKey ? 'text' : 'password'} bind:value={fields.braveKey} empty="Subscription token" inputClass="pr-9">
              {#snippet leading()}<KeyRound size={13} />{/snippet}
            </TextInput>
            <button type="button" class="absolute top-1/2 right-1.5 -translate-y-1/2 rounded-sm p-1 text-fg-faint hover:text-fg" onclick={() => (showKey = !showKey)} aria-label={showKey ? 'Hide token' : 'Show token'}>
              {#if showKey}<EyeOff size={14} />{:else}<Eye size={14} />{/if}
            </button>
          </div>
        </Field>
      {/if}
    </div>
  {:else if kind === ToolServerKind.STDIO}
    <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
      <Field label="Command" for="{idPrefix}-command" required error={problem.includes('Command') ? problem : undefined}>
        <TextInput id="{idPrefix}-command" mono bind:value={fields.command} empty="npx" />
      </Field>
      <Field label="Arguments" for="{idPrefix}-args" description="One per entry.">
        <IdList id="{idPrefix}-args" bind:items={fields.args} empty="-y @modelcontextprotocol/server-filesystem" />
      </Field>
    </div>
    <Field label="Environment" for="{idPrefix}-env" description="One NAME=value per line, added to the daemon's.">
      <TextArea id="{idPrefix}-env" mono height="h-20" bind:value={fields.env} empty={'HOME=/home/nebu\nTOKEN=secret'} />
    </Field>
  {:else if kind === ToolServerKind.HTTP}
    <Field label="URL" for="{idPrefix}-url" required error={problem.includes('URL') ? problem : undefined}>
      <TextInput id="{idPrefix}-url" mono bind:value={fields.url} empty="https://mcp.example/mcp" />
    </Field>
    <Field label="Headers" for="{idPrefix}-headers" description="One Name=value per line, sent with every request.">
      <TextArea id="{idPrefix}-headers" mono height="h-20" bind:value={fields.headers} empty={'Authorization=Bearer secret'} />
    </Field>
  {/if}
  {#if kind === ToolServerKind.STDIO || kind === ToolServerKind.HTTP}
    <Field label="Allowed tools" for="{idPrefix}-allowed" description="The server's own tool names. Every tool when empty.">
      <IdList id="{idPrefix}-allowed" bind:items={fields.allowedTools} empty="read_file" />
    </Field>
  {/if}
  {#if kind === ToolServerKind.FETCH}
    <SwitchRow bind:checked={fields.local} label="Read this host" description="Lets pages on this machine's own addresses be fetched. Off keeps models away from the daemon's own services." />
  {/if}
  <SwitchRow bind:checked={fields.enabled} label="Enabled" description="A server that is off keeps its settings and offers nothing." />
</div>
