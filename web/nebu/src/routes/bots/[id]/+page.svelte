<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api } from '$lib/api';
  import { tabState } from '$lib/tabs.svelte';
  import { live, clock, botByRef, botActivityOf, keepActivity } from '$lib/state.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { copyText } from '$lib/clipboard';
  import { when, ago, count } from '$lib/format';
  import { activityTone, botRunning, botStartable, botStoppable, postKinds } from '$lib/bots';
  import { ActionKind, BotState, type BotGuild } from '$proto/bot_pb';
  import { Link, Play, Square, Trash2, RefreshCw, Send, Bot as BotIcon } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Tabs from '$lib/components/ui/Tabs.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Stat from '$lib/components/ui/Stat.svelte';
  import Chip from '$lib/components/ui/Chip.svelte';
  import Copy from '$lib/components/ui/Copy.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import Select, { type SelectItem } from '$lib/components/ui/Select.svelte';
  import TextInput from '$lib/components/ui/TextInput.svelte';
  import TextArea from '$lib/components/ui/TextArea.svelte';
  import Segmented from '$lib/components/ui/Segmented.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';
  import BotForm, { type BotSection } from '$lib/components/bots/BotForm.svelte';

  const tabs = [
    { id: 'overview', label: 'Overview' },
    { id: 'activity', label: 'Activity' },
    { id: 'personas', label: 'Personas' },
    { id: 'automations', label: 'Automations' },
    { id: 'settings', label: 'Settings' }
  ];
  const formSections: BotSection[] = ['personas', 'automations', 'settings'];

  const id = $derived(page.params.id ?? '');
  const bot = $derived(botByRef(id));
  const tab = tabState(() => tabs.map((t) => t.id), () => 'overview');
  const loading = $derived(!live.ready && !live.error);
  const status = $derived(bot?.status);
  const running = $derived(botRunning(bot?.state));
  const shardsUp = $derived((status?.shards ?? []).filter((s) => s.connected).length);
  const shardsAll = $derived(status?.shards.length ?? 0);
  const activity = $derived(bot ? botActivityOf(bot.id) : []);
  let activityView = $state('all');
  const shownActivity = $derived(activityView === 'errors' ? activity.filter((a) => a.level === 'error') : activity);
  const errorCount = $derived(activity.filter((a) => a.level === 'error').length);
  const section = $derived(formSections.find((s) => s === tab.value));

  // Load history once per bot, then use the event stream.
  let backfilled = $state('');
  $effect(() => {
    if (tab.value !== 'activity' || !bot || backfilled === bot.id) return;
    const botId = bot.id;
    backfilled = botId;
    untrack(() => {
      api.bots
        .listBotActivity({ botId, limit: 500 })
        .then((r) => keepActivity(r.activity))
        .catch((err) => fail(err, 'Could not read activity'));
    });
  });

  let guilds = $state<BotGuild[]>([]);
  let guildsFor = $state('');
  let guildsLoading = $state(false);
  const postable = new Set(['text', 'news', 'thread']);
  async function loadGuilds() {
    if (!bot) return;
    guildsLoading = true;
    try {
      const r = await api.bots.listBotGuilds({ botId: bot.id });
      guilds = r.guilds;
      guildsFor = bot.id;
    } catch (err) {
      fail(err, 'Could not list guilds');
    } finally {
      guildsLoading = false;
    }
  }
  $effect(() => {
    if (bot && running && guildsFor !== bot.id && !guildsLoading) untrack(loadGuilds);
  });
  const channelItems = $derived.by((): SelectItem[] => {
    const out: SelectItem[] = [{ value: '', label: 'Enter a channel ID' }];
    for (const g of guilds) for (const c of g.channels) if (postable.has(c.kind)) out.push({ value: c.id, label: `#${c.name}`, detail: c.id, group: g.name });
    return out;
  });

  let postPersona = $state('');
  let postKind = $state(String(ActionKind.TEXT));
  let postChannel = $state('');
  let postContent = $state('');
  let posting = $state(false);
  const personaItems = $derived([{ value: '', label: bot?.spec?.personas[0]?.name ? `${bot.spec.personas[0].name} (default)` : 'Default persona' }, ...(bot?.spec?.personas ?? []).map((p) => ({ value: p.id, label: p.name }))]);
  const canPost = $derived(running && postChannel.trim() !== '' && postContent.trim() !== '' && !posting);

  async function post() {
    if (!bot) return;
    posting = true;
    try {
      const r = await api.bots.sendBotMessage({ botId: bot.id, channelId: postChannel.trim(), personaId: postPersona, kind: Number(postKind) as ActionKind, content: postContent });
      ok('Posted', `Message ${r.messageId}`, r.trace ? { href: `/requests?id=${encodeURIComponent(r.trace)}`, label: 'Open trace' } : undefined);
      postContent = '';
    } catch (err) {
      fail(err, 'Post failed');
    } finally {
      posting = false;
    }
  }

  let switching = $state(false);
  async function start() {
    if (!bot) return;
    switching = true;
    try {
      const r = await api.bots.startBot({ id: bot.id });
      if (r.bot) live.bots.set(r.bot.id, r.bot);
      ok(`Starting ${bot.name}`);
    } catch (err) {
      fail(err, 'Start failed');
    } finally {
      switching = false;
    }
  }
  async function stop() {
    if (!bot) return;
    switching = true;
    try {
      const r = await api.bots.stopBot({ id: bot.id });
      if (r.bot) live.bots.set(r.bot.id, r.bot);
      ok(`Stopped ${bot.name}`);
    } catch (err) {
      fail(err, 'Stop failed');
    } finally {
      switching = false;
    }
  }
  async function remove() {
    if (!bot) return;
    if (!(await confirm({ title: `Delete bot ${bot.name}?`, message: 'Disconnects the bot and deletes its settings, personas, and automations. Keeps the Discord application.', action: 'Delete', tone: 'bad' }))) return;
    try {
      await api.bots.deleteBot({ id: bot.id });
      live.bots.delete(bot.id);
      ok(`Deleted ${bot.name}`);
      goto('/bots');
    } catch (err) {
      fail(err, 'Delete failed');
    }
  }

  async function copyInvite() {
    if (status?.inviteUrl && (await copyText(status.inviteUrl))) ok('Invite link copied');
  }
</script>

{#if loading}
  <div class="skeleton h-40" aria-busy="true"></div>
{:else if !bot}
  <PageHeader title="Bot not found" back={{ href: '/bots', label: 'Bots' }} />
  <Empty title="No bot named {id}">
    <Button href="/bots">Back to Bots</Button>
  </Empty>
{:else}
  <PageHeader title={bot.name} back={{ href: '/bots', label: 'Bots' }}>
    {#snippet meta()}
      <State values={BotState} value={bot.state} />
      {#if status?.username}<span class="font-mono text-fg-muted">@{status.username}</span>{/if}
      {#if status && running}
        <span>{count(status.guilds)} {status.guilds === 1 ? 'guild' : 'guilds'}</span>
        <span>{shardsUp}/{shardsAll} shards</span>
      {/if}
    {/snippet}
    {#if botStartable(bot.state)}
      <Button variant="primary" icon={Play} loading={switching} onclick={start}>Start</Button>
    {:else if botStoppable(bot.state)}
      <Button icon={Square} loading={switching} onclick={stop}>Stop</Button>
    {/if}
    <Menu
      items={[
        { label: 'Copy invite link', icon: Link, onSelect: copyInvite, disabled: !status?.inviteUrl, detail: status?.inviteUrl ? 'Invite to a server' : 'Available after connecting' },
        { label: '', separator: true },
        { label: 'Delete bot', icon: Trash2, tone: 'bad', onSelect: remove }
      ]}
    />
    {#snippet below()}
      <Tabs tabs={tabs.map((t) => (t.id === 'activity' && errorCount ? { ...t, count: errorCount } : t.id === 'personas' ? { ...t, count: bot.spec?.personas.length || undefined } : t.id === 'automations' ? { ...t, count: bot.spec?.automations.length || undefined } : t))} bind:value={tab.value} />
    {/snippet}
  </PageHeader>

  {#if tab.value === 'overview'}
    <div class="flex flex-col gap-5">
      {#if bot.error && bot.state === BotState.FAILED}<div class="note note-bad">{bot.error}</div>{/if}

      <div class="grid grid-cols-1 gap-5 xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <Card title="Connection">
          <div class="flex flex-col gap-4">
            <Kv
              columns={2}
              items={[
                ['Username', status?.username ? `@${status.username}` : ''],
                ['User ID', status?.userId],
                ['Application ID', status?.applicationId],
                ['Recommended shards', status?.recommendedShards || ''],
                ['Started', running || bot.state === BotState.CONNECTING ? when(status?.startedAt) : ''],
                ['Token', bot.tokenSet ? 'Stored' : 'Missing']
              ]}
            />
            <div class="flex items-center gap-1 text-sm">
              <span class="whitespace-nowrap text-fg-faint">Invite link</span>
              {#if status?.inviteUrl}
                <a class="link ml-5 truncate font-mono text-xs" href={status.inviteUrl} target="_blank" rel="noreferrer">{status.inviteUrl}</a>
                <Copy text={status.inviteUrl} label="Copy invite link" />
              {:else}
                <span class="ml-5 text-fg-muted">Available after connecting</span>
              {/if}
            </div>
            <div class="grid grid-cols-2 gap-4 sm:grid-cols-4">
              <Stat label="Replies" value={count(status?.replies)} />
              <Stat label="Images" value={count(status?.images)} />
              <Stat label="Videos" value={count(status?.videos)} />
              <Stat label="Errors" value={count(status?.errors)} tone={status?.errors ? 'bad' : 'default'} />
            </div>
          </div>
        </Card>

        <Card title="Shards" padded={false}>
          {#if !status?.shards.length}
            <div class="p-4"><Empty compact title={running ? 'No shards reported' : 'Start the bot to view shards'} /></div>
          {:else}
            <div class="overflow-x-auto">
              <table class="tbl dense">
                <thead><tr><th>Shard</th><th>State</th><th class="num">Guilds</th><th class="num">Latency</th><th>Connected</th><th>Error</th></tr></thead>
                <tbody>
                  {#each status.shards as s (s.id)}
                    <tr>
                      <td class="font-mono text-xs">{s.id}</td>
                      <td><State tone={s.connected ? 'ok' : 'bad'} label={s.connected ? 'Connected' : 'Down'} /></td>
                      <td class="num">{count(s.guilds)}</td>
                      <td class="num">{s.connected ? `${s.latencyMs} ms` : '-'}</td>
                      <td class="text-fg-muted" title={when(s.connectedAt)}>{s.connected ? ago(s.connectedAt, clock.now) : '-'}</td>
                      <td class="max-w-xs truncate text-xs text-bad" title={s.error}>{s.error}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          {/if}
        </Card>
      </div>

      <div class="grid grid-cols-1 gap-5 xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <Card title="Guilds" meta={guildsFor === bot.id && guilds.length ? `${guilds.length}` : undefined}>
          {#snippet actions()}
            <Button size="sm" variant="ghost" icon={RefreshCw} loading={guildsLoading} disabled={!running} onclick={loadGuilds}>Refresh</Button>
          {/snippet}
          {#if !running}
            <Empty compact title="Connect to view guilds and channels" />
          {:else if guildsLoading && guildsFor !== bot.id}
            <div class="flex items-center gap-2 py-4 text-sm text-fg-muted"><Spinner size={15} /> Loading</div>
          {:else if guilds.length === 0}
            <Empty compact title="No guilds. Use the invite link above." />
          {:else}
            <div class="flex flex-col gap-4">
              {#each guilds as g (g.id)}
                {@const channels = g.channels.filter((c) => postable.has(c.kind))}
                <div class="flex flex-col gap-2">
                  <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
                    <span class="text-sm font-medium text-fg">{g.name}</span>
                    <span class="text-xs text-fg-faint">{count(g.members)} members</span>
                    <span class="inline-flex items-center gap-0.5 font-mono text-xs text-fg-faint">{g.id}<Copy text={g.id} label="Copy guild ID" size={12} /></span>
                  </div>
                  {#if channels.length === 0}
                    <span class="text-xs text-fg-faint">No visible text channels</span>
                  {:else}
                    <div class="flex flex-wrap gap-x-3 gap-y-1">
                      {#each channels as c (c.id)}
                        <span class="inline-flex items-center gap-1">
                          <Chip text="#{c.name}" mono={false} title={c.kind} />
                          <span class="font-mono text-xs text-fg-faint">{c.id}</span>
                          <Copy text={c.id} label="Copy channel ID" size={12} />
                        </span>
                      {/each}
                    </div>
                  {/if}
                </div>
              {/each}
            </div>
          {/if}
        </Card>

        <Card title="Post now">
          <form
            class="flex flex-col gap-4"
            onsubmit={(e) => {
              e.preventDefault();
              if (canPost) post();
            }}
          >
            <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
              <Field label="Persona" for="post-persona">
                <Select id="post-persona" bind:value={postPersona} items={personaItems} disabled={!running} />
              </Field>
              <Field label="Kind" for="post-kind">
                <Select id="post-kind" bind:value={postKind} items={postKinds} disabled={!running} />
              </Field>
              {#if guildsFor === bot.id && guilds.length}
                <Field label="Channel" for="post-channel-pick">
                  <Select id="post-channel-pick" bind:value={postChannel} items={channelItems} disabled={!running} />
                </Field>
              {/if}
              <Field label="Channel ID" for="post-channel">
                <TextInput id="post-channel" mono bind:value={postChannel} empty="Channel ID" disabled={!running} />
              </Field>
            </div>
            <Field label={Number(postKind) === ActionKind.TEXT ? 'Message' : 'Prompt'} for="post-content">
              <TextArea id="post-content" bind:value={postContent} disabled={!running} />
            </Field>
            <div class="flex items-center justify-end gap-3">
              {#if !running}<span class="mr-auto text-xs text-fg-faint">Start the bot to post.</span>{/if}
              <Button type="submit" variant="primary" icon={Send} loading={posting} disabled={!canPost}>Send</Button>
            </div>
          </form>
        </Card>
      </div>
    </div>
  {:else if tab.value === 'activity'}
    <div class="flex flex-col gap-4">
      <div class="flex items-center gap-3">
        <Segmented bind:value={activityView} tabs={[{ id: 'all', label: 'All', count: activity.length || undefined }, { id: 'errors', label: 'Errors', count: errorCount || undefined }]} />
        <span class="text-xs text-fg-faint">Live, latest 500 events</span>
      </div>
      {#if shownActivity.length === 0}
        <Empty icon={BotIcon} title={activityView === 'errors' ? 'No errors' : 'No activity'} />
      {:else}
        <div class="overflow-x-auto">
          <table class="tbl dense">
            <thead><tr><th>When</th><th>Level</th><th>Kind</th><th>Persona</th><th>Channel</th><th>Message</th><th></th></tr></thead>
            <tbody>
              {#each shownActivity as a (`${a.at?.seconds}.${a.at?.nanos}.${a.kind}.${a.message}`)}
                <tr>
                  <td class="whitespace-nowrap text-fg-muted" title={when(a.at)}>{ago(a.at, clock.now)}</td>
                  <td><State tone={activityTone(a.level)} label={a.level || 'info'} /></td>
                  <td><Chip text={a.kind} mono={false} /></td>
                  <td class="whitespace-nowrap text-fg-muted">{a.persona || '-'}</td>
                  <td class="font-mono text-xs text-fg-faint">{a.channelId || '-'}</td>
                  <td class="min-w-64 text-fg [overflow-wrap:anywhere]">{a.message}</td>
                  <td class="actions">
                    <span>
                      {#if a.trace}<a class="link text-xs whitespace-nowrap" href="/requests?id={encodeURIComponent(a.trace)}">Trace</a>{/if}
                    </span>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </div>
  {:else if section}
    {#key bot.id + (bot.updatedAt?.seconds ?? 0n)}
      <BotForm {bot} {section} cancelHref="/bots/{bot.id}" onSaved={() => (tab.value = 'overview')} />
    {/key}
  {/if}
{/if}
