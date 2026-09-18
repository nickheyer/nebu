<script lang="ts" module>
  export type BotSection = 'personas' | 'automations' | 'settings';
</script>

<script lang="ts">
  import { api } from '$lib/api';
  import { live } from '$lib/state.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { activityTypes, automationFields, botRunning, commandBlurbs, commandRoles, personaFields, presenceStatuses, specFields, specFrom, triggerKinds, actionKinds, type CommandRole } from '$lib/bots';
  import { BotState, type Bot, type ProbeBotTokenResponse } from '$proto/bot_pb';
  import { ArrowDown, ArrowUp, ChevronDown, ChevronRight, Eye, EyeOff, KeyRound, Plus, Trash2 } from '@lucide/svelte';
  import Field from '../ui/Field.svelte';
  import TextInput from '../ui/TextInput.svelte';
  import NumberInput from '../ui/NumberInput.svelte';
  import Select from '../ui/Select.svelte';
  import Button from '../ui/Button.svelte';
  import IconButton from '../ui/IconButton.svelte';
  import Card from '../ui/Card.svelte';
  import Chip from '../ui/Chip.svelte';
  import Checkbox from '../ui/Checkbox.svelte';
  import Switch from '../ui/Switch.svelte';
  import Empty from '../ui/Empty.svelte';
  import Kv from '../ui/Kv.svelte';
  import Copy from '../ui/Copy.svelte';
  import SwitchRow from './SwitchRow.svelte';
  import IdList from './IdList.svelte';
  import PersonaEditor from './PersonaEditor.svelte';
  import AutomationEditor from './AutomationEditor.svelte';
  import ChannelPicker from './ChannelPicker.svelte';

  // Keep all tabs in one form so a save includes every setting.
  let { bot, section, cancelHref = '/bots', onSaved }: { bot?: Bot; section: BotSection; cancelHref?: string; onSaved: (bot: Bot) => void } = $props();

  // The page remounts the form when the bot changes.
  function initial() {
    const personas = bot?.spec?.personas ?? [];
    return {
      name: bot?.name ?? '',
      enabled: bot?.enabled ?? true,
      fields: specFields(bot?.spec),
      openPersonas: Object.fromEntries(personas.map((p) => [p.id, personas.length === 1])) as Record<string, boolean>
    };
  }
  const start = initial();
  let name = $state(start.name);
  let token = $state('');
  let showToken = $state(false);
  let enabled = $state(start.enabled);
  let fields = $state(start.fields);
  let saving = $state(false);
  let probing = $state(false);
  let probe = $state<ProbeBotTokenResponse | null>(null);
  let openPersonas = $state<Record<string, boolean>>(start.openPersonas);
  let openAutomations = $state<Record<string, boolean>>({});

  const creating = $derived(!bot);
  const pickerBot = $derived(bot && botRunning(bot.state) ? bot.id : '');
  const badName = $derived(name.trim() === '');
  const nameTaken = $derived(name.trim() !== bot?.name && [...live.bots.values()].some((b) => b.name === name.trim()));
  const needToken = $derived(creating && token.trim() === '');
  const badShards = $derived(fields.shardIds.length > 0 && !(parseInt(fields.shardCount, 10) > 0));
  const personasOk = $derived(fields.personas.every((p) => p.name.trim() !== ''));
  const formOk = $derived(!badName && !nameTaken && !needToken && !badShards && personasOk);
  const canProbe = $derived(token.trim() !== '' || !!bot?.tokenSet);

  const triggerLabel = (kind: string) => triggerKinds.find((t) => t.value === kind)?.label ?? 'Trigger';
  const actionLabel = (kind: string) => actionKinds.find((a) => a.value === kind)?.label ?? 'Action';
  const personaName = (id: string) => (id ? (fields.personas.find((p) => p.id === id)?.name ?? 'Missing persona') : fields.personas[0]?.name ?? 'Default persona');

  async function testToken() {
    probing = true;
    try {
      probe = await api.bots.probeBotToken({ token: token.trim(), botId: token.trim() ? '' : (bot?.id ?? '') });
      ok(`Token belongs to @${probe.username}`);
    } catch (err) {
      probe = null;
      fail(err, 'Token check failed');
    } finally {
      probing = false;
    }
  }

  function addPersona() {
    const p = personaFields();
    fields.personas.push(p);
    openPersonas[p.id] = true;
  }

  async function removePersona(i: number) {
    const p = fields.personas[i];
    if (!(await confirm({ title: `Remove persona ${p.name.trim() || i + 1}?`, message: 'Affected automations use the default persona after saving.', action: 'Remove', tone: 'bad' }))) return;
    fields.personas.splice(i, 1);
    for (const a of fields.automations) if (a.personaId === p.id) a.personaId = '';
  }

  function movePersona(i: number, by: number) {
    const j = i + by;
    if (j < 0 || j >= fields.personas.length) return;
    const [p] = fields.personas.splice(i, 1);
    fields.personas.splice(j, 0, p);
  }

  function addAutomation() {
    const a = automationFields();
    fields.automations.push(a);
    openAutomations[a.id] = true;
  }

  async function removeAutomation(i: number) {
    const a = fields.automations[i];
    if (!(await confirm({ title: `Remove automation ${a.name.trim() || i + 1}?`, message: 'Takes effect on save.', action: 'Remove', tone: 'bad' }))) return;
    fields.automations.splice(i, 1);
  }

  function setRole(role: CommandRole, registered: boolean) {
    fields.commands.disabled = registered ? fields.commands.disabled.filter((r) => r !== role) : [...fields.commands.disabled.filter((r) => r !== role), role];
  }

  function addIds(list: string[], ids: string[]): string[] {
    return [...list, ...ids.filter((id) => !list.includes(id))];
  }

  async function save() {
    saving = true;
    const spec = specFrom(fields);
    try {
      if (bot) {
        const r = await api.bots.updateBot({ id: bot.id, name: name.trim(), token: token.trim(), enabled, spec });
        ok(`Saved ${name.trim()}`);
        if (r.bot) {
          live.bots.set(r.bot.id, r.bot);
          onSaved(r.bot);
        }
      } else {
        const r = await api.bots.createBot({ name: name.trim(), token: token.trim(), enabled, spec });
        ok(`Created ${name.trim()}`);
        if (r.bot) {
          live.bots.set(r.bot.id, r.bot);
          onSaved(r.bot);
        }
      }
    } catch (err) {
      fail(err, bot ? 'Save failed' : 'Create failed');
    } finally {
      saving = false;
    }
  }
</script>

<form
  class="flex flex-col gap-5"
  onsubmit={(e) => {
    e.preventDefault();
    if (formOk && !saving) save();
  }}
>
  {#if section === 'settings'}
    <Card title="Bot">
      <div class="flex flex-col gap-4">
        <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
          <Field label="Name" for="bot-name" required error={nameTaken ? 'Another bot has this name' : undefined}>
            <TextInput id="bot-name" bind:value={name} empty="ada" invalid={badName || nameTaken} />
          </Field>
          <Field label="Token" for="bot-token" required={creating} description={bot?.tokenSet ? 'Stored on the daemon.' : 'Discord Developer Portal > Bot > Token.'}>
            <div class="flex items-center gap-2">
              <div class="relative flex-1">
                <TextInput id="bot-token" mono type={showToken ? 'text' : 'password'} bind:value={token} inputClass="pr-9" empty={bot?.tokenSet ? 'Leave empty to keep current token' : ''} autocomplete="off">
                  {#snippet leading()}<KeyRound size={13} />{/snippet}
                </TextInput>
                <button type="button" class="absolute top-1/2 right-1.5 -translate-y-1/2 rounded-sm p-1 text-fg-faint hover:text-fg" onclick={() => (showToken = !showToken)} aria-label={showToken ? 'Hide token' : 'Show token'}>
                  {#if showToken}<EyeOff size={14} />{:else}<Eye size={14} />{/if}
                </button>
              </div>
              <Button type="button" loading={probing} disabled={!canProbe} onclick={testToken}>Test token</Button>
            </div>
          </Field>
        </div>
        {#if probe}
          <div class="rounded-md border border-line bg-sunken/40 px-4 py-3">
            <Kv
              columns={2}
              items={[
                ['Username', `@${probe.username}`],
                ['Application ID', probe.applicationId],
                ['User ID', probe.userId],
                ['Recommended shards', probe.recommendedShards]
              ]}
            />
            {#if probe.inviteUrl}
              <div class="mt-2 flex items-center gap-1 text-sm">
                <span class="whitespace-nowrap text-fg-faint">Invite link</span>
                <a class="link ml-4 truncate font-mono text-xs" href={probe.inviteUrl} target="_blank" rel="noreferrer">{probe.inviteUrl}</a>
                <Copy text={probe.inviteUrl} label="Copy invite link" />
              </div>
            {/if}
          </div>
        {/if}
        <SwitchRow bind:checked={enabled} label="Connect on save" description="Reconnects after daemon restarts." />
        <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
          <Field label="Shards" for="bot-shards" description="Uses Discord's recommendation when empty." error={badShards ? 'Set a count when using shard IDs' : undefined}>
            <NumberInput id="bot-shards" integer min={0} bind:value={fields.shardCount} empty={probe?.recommendedShards ? String(probe.recommendedShards) : bot?.status?.recommendedShards ? String(bot.status.recommendedShards) : 'recommended'} invalid={badShards} />
          </Field>
          <Field label="Shard IDs" for="bot-shard-ids" description="All shards when empty. Set for multiple hosts.">
            <IdList id="bot-shard-ids" numeric bind:items={fields.shardIds} empty="0" />
          </Field>
        </div>
      </div>
    </Card>

    <Card title="Presence">
      <div class="flex flex-col gap-4">
        <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-3">
          <Field label="Status" for="presence-status">
            <Select id="presence-status" bind:value={fields.presence.status} items={presenceStatuses} />
          </Field>
          <Field label="Activity" for="presence-type">
            <Select id="presence-type" bind:value={fields.presence.activityType} items={activityTypes} />
          </Field>
          <Field label="Rotate every" for="presence-rotate">
            <NumberInput id="presence-rotate" integer min={0} step={30} unit="s" bind:value={fields.presence.rotateSeconds} empty="300" />
          </Field>
        </div>
        <Field label="Activity lines" for="presence-activities">
          <IdList id="presence-activities" mono={false} bind:items={fields.presence.activities} empty="Activity text" />
        </Field>
      </div>
    </Card>

    <Card title="Engagement">
      <div class="flex flex-col gap-4">
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <SwitchRow bind:checked={fields.engagement.directMessages} label="Reply to DMs" />
          <SwitchRow bind:checked={fields.engagement.requireMention} label="Require mention" description="In guilds, require a mention, reply, or wake word." />
          <SwitchRow bind:checked={fields.engagement.answerBots} label="Reply to bots" description="Includes webhooks and this bot's personas." />
          <SwitchRow bind:checked={fields.engagement.memberEvents} label="Member events" description="Requires Server Members intent in the Discord Developer Portal." />
        </div>
        <div class="grid grid-cols-2 gap-x-5 gap-y-4 lg:grid-cols-5">
          <Field label="Command prefix" for="engagement-prefix">
            <TextInput id="engagement-prefix" mono bind:value={fields.engagement.prefix} empty="!" maxlength={8} />
          </Field>
          <Field label="Channel cooldown" for="engagement-cooldown">
            <NumberInput id="engagement-cooldown" integer min={0} step={1000} unit="ms" bind:value={fields.engagement.cooldownMs} empty="0" />
          </Field>
          <Field label="User cooldown" for="engagement-user-cooldown">
            <NumberInput id="engagement-user-cooldown" integer min={0} step={1000} unit="ms" bind:value={fields.engagement.userCooldownMs} empty="0" />
          </Field>
          <Field label="Concurrent generations" for="engagement-concurrent">
            <NumberInput id="engagement-concurrent" integer min={0} bind:value={fields.engagement.maxConcurrent} empty="4" />
          </Field>
          <Field label="Bot chain limit" for="engagement-chain" description="Consecutive bot messages.">
            <NumberInput id="engagement-chain" integer min={0} bind:value={fields.engagement.maxBotChain} empty="3" />
          </Field>
        </div>
        <p class="text-xs leading-5 text-fg-muted">Enable Developer Mode in Discord's Settings > Advanced, then right click to copy IDs.</p>
        <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
          <Field label="Allowed channels" for="engagement-channels" description="All channels when empty.">
            <IdList id="engagement-channels" bind:items={fields.engagement.channelIds} empty="Channel ID">
              {#if pickerBot}<ChannelPicker botId={pickerBot} onPick={(ids) => (fields.engagement.channelIds = addIds(fields.engagement.channelIds, ids))} />{/if}
            </IdList>
          </Field>
          <Field label="Blocked channels" for="engagement-denied-channels">
            <IdList id="engagement-denied-channels" bind:items={fields.engagement.deniedChannelIds} empty="Channel ID">
              {#if pickerBot}<ChannelPicker botId={pickerBot} onPick={(ids) => (fields.engagement.deniedChannelIds = addIds(fields.engagement.deniedChannelIds, ids))} />{/if}
            </IdList>
          </Field>
          <Field label="Allowed guilds" for="engagement-guilds" description="All guilds when empty.">
            <IdList id="engagement-guilds" bind:items={fields.engagement.guildIds} empty="Guild ID">
              {#if pickerBot}<ChannelPicker botId={pickerBot} guilds onPick={(ids) => (fields.engagement.guildIds = addIds(fields.engagement.guildIds, ids))} />{/if}
            </IdList>
          </Field>
          <Field label="Allowed users" for="engagement-users" description="Everyone when empty.">
            <IdList id="engagement-users" bind:items={fields.engagement.userIds} empty="User ID" />
          </Field>
          <Field label="Blocked users" for="engagement-denied-users">
            <IdList id="engagement-denied-users" bind:items={fields.engagement.deniedUserIds} empty="User ID" />
          </Field>
        </div>
      </div>
    </Card>

    <Card title="Commands">
      <div class="flex flex-col gap-4">
        <SwitchRow bind:checked={fields.commands.enabled} label="Slash commands" />
        {#if fields.commands.enabled}
          <Field label="Guilds" for="commands-guilds" description="Global when empty. Guild registration is immediate.">
            <IdList id="commands-guilds" bind:items={fields.commands.guildIds} empty="Guild ID">
              {#if pickerBot}<ChannelPicker botId={pickerBot} guilds onPick={(ids) => (fields.commands.guildIds = addIds(fields.commands.guildIds, ids))} />{/if}
            </IdList>
          </Field>
          <div class="overflow-x-auto">
            <table class="tbl dense">
              <thead><tr><th>Command</th><th>Name</th><th>Register</th></tr></thead>
              <tbody>
                {#each commandRoles as role (role)}
                  <tr>
                    <td class="whitespace-nowrap">
                      <div class="text-sm text-fg">/{role}</div>
                      <div class="text-xs text-fg-faint">{commandBlurbs[role]}</div>
                    </td>
                    <td class="w-64"><TextInput id="command-{role}" mono size="sm" bind:value={fields.commands.names[role]} empty={role} maxlength={32} aria-label="Name of {role}" /></td>
                    <td><Checkbox label="Register" checked={!fields.commands.disabled.includes(role)} onchange={(on) => setRole(role, on)} /></td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </div>
    </Card>

    <Card title="Memory">
      <div class="flex flex-col gap-4">
        <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-3">
          <Field label="Messages" for="memory-messages" description="Channel history, up to 100.">
            <NumberInput id="memory-messages" integer min={0} max={100} bind:value={fields.memory.messages} empty="20" />
          </Field>
          <Field label="Max message age" for="memory-window">
            <NumberInput id="memory-window" integer min={0} step={15} unit="min" bind:value={fields.memory.windowMinutes} empty="all" />
          </Field>
          <Field label="Max characters" for="memory-chars" description="Oldest messages are dropped first.">
            <NumberInput id="memory-chars" integer min={0} step={1000} bind:value={fields.memory.maxChars} empty="8000" />
          </Field>
        </div>
        <SwitchRow bind:checked={fields.memory.includeNames} label="Include display names" />
      </div>
    </Card>

    <Card title="Media">
      <div class="grid grid-cols-2 gap-x-5 gap-y-4 lg:grid-cols-4">
        <Field label="Video frames" for="media-frames" description="Sampled from attachments. Requires ffmpeg on the daemon host.">
          <NumberInput id="media-frames" integer min={0} max={32} bind:value={fields.media.videoFrames} empty="0" />
        </Field>
        <Field label="Upload limit" for="media-upload">
          <NumberInput id="media-upload" min={0} step={1} unit="MB" bind:value={fields.media.maxUploadMb} empty="10" />
        </Field>
        <Field label="Image size" for="media-image-size" description="Multiples of 16.">
          <TextInput id="media-image-size" mono bind:value={fields.media.imageSize} empty="1024x1024" />
        </Field>
        <Field label="Image steps" for="media-image-steps">
          <NumberInput id="media-image-steps" integer min={0} bind:value={fields.media.imageSteps} empty="model" />
        </Field>
        <Field label="Images per request" for="media-image-count" description="Up to 4.">
          <NumberInput id="media-image-count" integer min={0} max={4} bind:value={fields.media.imageCount} empty="1" />
        </Field>
        <Field label="Video size" for="media-video-size" description="WIDTHxHEIGHT.">
          <TextInput id="media-video-size" mono bind:value={fields.media.videoSize} empty="model" />
        </Field>
        <Field label="Video length" for="media-video-seconds">
          <NumberInput id="media-video-seconds" min={0} step={0.5} unit="s" bind:value={fields.media.videoSeconds} empty="model" />
        </Field>
        <Field label="Video rate" for="media-video-fps">
          <NumberInput id="media-video-fps" integer min={0} unit="fps" bind:value={fields.media.videoFps} empty="model" />
        </Field>
      </div>
    </Card>
  {:else if section === 'personas'}
    {#if fields.personas.length === 0}
      <Empty title="No personas">
        <Button size="sm" variant="primary" icon={Plus} onclick={addPersona}>Add persona</Button>
      </Empty>
    {:else}
      {#each fields.personas as p, i (p.id)}
        {@const open = !!openPersonas[p.id]}
        <section class="card flex flex-col">
          <header class="flex min-h-11 items-center gap-2.5 px-4 {open ? 'border-b border-line' : ''}">
            <button type="button" class="flex min-w-0 flex-1 items-center gap-2 py-2 text-left" aria-expanded={open} onclick={() => (openPersonas[p.id] = !open)}>
              {#if open}<ChevronDown size={14} class="shrink-0 text-fg-faint" />{:else}<ChevronRight size={14} class="shrink-0 text-fg-faint" />{/if}
              <span class="truncate text-sm font-semibold text-fg">{p.name.trim() || `Persona ${i + 1}`}</span>
              {#if i === 0}<Chip text="Default" mono={false} title="Used when no persona is selected" />{/if}
              {#if p.webhook}<Chip text="webhook" title="Uses a channel webhook" />{/if}
              {#if p.humanize.enabled}<Chip text="humanized" title="Typing delays and reply controls" />{/if}
              {#if p.wakeWords.length}<span class="truncate text-xs text-fg-faint">{p.wakeWords.join(', ')}</span>{/if}
            </button>
            <div class="flex shrink-0 items-center gap-0.5">
              <IconButton icon={ArrowUp} label="Move up" size="sm" disabled={i === 0} onclick={() => movePersona(i, -1)} />
              <IconButton icon={ArrowDown} label="Move down" size="sm" disabled={i === fields.personas.length - 1} onclick={() => movePersona(i, 1)} />
              <IconButton icon={Trash2} label="Remove persona" size="sm" onclick={() => removePersona(i)} />
            </div>
          </header>
          {#if open}
            <div class="p-4"><PersonaEditor bind:persona={fields.personas[i]} idPrefix="persona-{p.id}" {pickerBot} /></div>
          {/if}
        </section>
      {/each}
      <div><Button icon={Plus} onclick={addPersona}>Add persona</Button></div>
    {/if}
  {:else if section === 'automations'}
    {#if fields.automations.length === 0}
      <Empty title="No automations">
        <Button size="sm" variant="primary" icon={Plus} onclick={addAutomation}>Add automation</Button>
      </Empty>
    {:else}
      {#each fields.automations as a, i (a.id)}
        {@const open = !!openAutomations[a.id]}
        <section class="card flex flex-col">
          <header class="flex min-h-11 items-center gap-2.5 px-4 {open ? 'border-b border-line' : ''}">
            <Switch bind:checked={fields.automations[i].enabled} label="Enabled" />
            <button type="button" class="flex min-w-0 flex-1 items-center gap-2 py-2 text-left" aria-expanded={open} onclick={() => (openAutomations[a.id] = !open)}>
              {#if open}<ChevronDown size={14} class="shrink-0 text-fg-faint" />{:else}<ChevronRight size={14} class="shrink-0 text-fg-faint" />{/if}
              <span class="truncate text-sm font-semibold {a.enabled ? 'text-fg' : 'text-fg-muted'}">{a.name.trim() || `Automation ${i + 1}`}</span>
              <span class="truncate text-xs text-fg-faint">{triggerLabel(a.triggerKind)}: {actionLabel(a.actionKind)} / {personaName(a.personaId)}</span>
            </button>
            <IconButton icon={Trash2} label="Remove automation" size="sm" onclick={() => removeAutomation(i)} />
          </header>
          {#if open}
            <div class="p-4"><AutomationEditor bind:automation={fields.automations[i]} personas={fields.personas} idPrefix="automation-{a.id}" {pickerBot} /></div>
          {/if}
        </section>
      {/each}
      <div><Button icon={Plus} onclick={addAutomation}>Add automation</Button></div>
    {/if}
  {/if}

  <div class="sticky bottom-0 -mx-5 flex items-center justify-end gap-2 border-t border-line bg-bg px-5 py-3 sm:-mx-6 sm:px-6 lg:-mx-8 lg:px-8">
    {#if bot && !creating}<span class="mr-auto text-xs text-fg-faint">{botRunning(bot.state) || bot.state === BotState.CONNECTING ? 'Saving restarts the bot.' : 'Saves all tabs.'}</span>{/if}
    <Button variant="ghost" href={cancelHref}>Cancel</Button>
    <Button type="submit" variant="primary" loading={saving} disabled={!formOk}>{creating ? 'Create bot' : 'Save'}</Button>
  </div>
</form>
