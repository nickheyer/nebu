<script lang="ts">
  import { onMount } from 'svelte';
  import { Code } from '@connectrpc/connect';
  import { api, baseUrl, code, setToken, token, message } from '$lib/api';
  import { connect, live, cached, desktopNotify, setDesktopNotify, updateSettings } from '$lib/state.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { sourceLabel } from '$lib/catalog';
  import type { Provider, SourceStatus } from '$proto/source_pb';
  import { KeyRound, Eye, EyeOff, Plug, Plus, Pencil, Trash2, ExternalLink, Check, Circle, CircleAlert } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import Info from '$lib/components/ui/Info.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Switch from '$lib/components/ui/Switch.svelte';
  import Tip from '$lib/components/ui/Tip.svelte';
  import SkeletonRows from '$lib/components/ui/SkeletonRows.svelte';
  import SourceDialog from '$lib/components/SourceDialog.svelte';

  let value = $state(token());
  let show = $state(false);
  let notify = $state(desktopNotify());
  let testing = $state(false);
  let providers = $state<Provider[]>([]);
  let dialogOpen = $state(false);
  let editing = $state<SourceStatus | null>(null);
  let label = $state('');
  let labelSaving = $state(false);
  let labelSeeded = false;

  const statuses = $derived(cached.sources);
  const labelDirty = $derived(label.trim() !== (live.settings?.hostLabel ?? ''));
  const tokenDirty = $derived(value.trim() !== token());

  // The label field starts from the daemon's value once it arrives
  $effect(() => {
    if (live.settings && !labelSeeded) {
      label = live.settings.hostLabel;
      labelSeeded = true;
    }
  });

  onMount(() => {
    api.sources
      .listProviders({})
      .then((p) => (providers = p.providers))
      .catch((err) => fail(err, 'Could not list providers'));
  });

  async function saveLabel() {
    labelSaving = true;
    if (await updateSettings({ hostLabel: label.trim() })) ok(label.trim() ? `This host is ${label.trim()}` : 'Label cleared');
    labelSaving = false;
  }

  async function toggleNotify(on: boolean) {
    notify = await setDesktopNotify(on);
    if (on && !notify) fail(new Error('the browser refused permission'), 'Notifications stay off');
  }

  function save() {
    setToken(value.trim());
    connect();
    ok(value.trim() ? 'Token saved' : 'Token cleared');
  }

  async function test() {
    testing = true;
    try {
      const r = await api.host.getProfile({});
      ok('Connected', r.profile?.hostname);
    } catch (err) {
      fail(err, 'Connection failed');
    } finally {
      testing = false;
    }
  }

  function add() {
    editing = null;
    dialogOpen = true;
  }

  function edit(s: SourceStatus) {
    editing = s;
    dialogOpen = true;
  }

  async function remove(s: SourceStatus) {
    const name = sourceLabel(s);
    const yes = await confirm({ title: `Remove ${name}?`, message: 'Models it pulled stay in the library.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.sources.deleteSource({ id: s.source?.id ?? '' });
      ok(`Removed ${name}`);
    } catch (err) {
      if (code(err) !== Code.FailedPrecondition) {
        fail(err, 'Remove failed');
        return;
      }
      // The daemon names the watches and wants on the source, dropping them is the person's call
      const force = await confirm({ title: `Remove ${name} and what names it?`, message: message(err), action: 'Remove all', tone: 'bad' });
      if (!force) return;
      try {
        await api.sources.deleteSource({ id: s.source?.id ?? '', force: true });
        ok(`Removed ${name}`);
      } catch (again) {
        fail(again, 'Remove failed');
      }
    }
  }

  // The settings a source carries, defaults left out, for the row summary
  function summary(s: SourceStatus): string {
    const parts: string[] = [];
    for (const f of s.capabilities?.fields ?? []) {
      const v = s.source?.config[f.name];
      if (v) parts.push(`${f.name}=${v}`);
    }
    return parts.join('  ');
  }
</script>

{#snippet head()}
  <thead><tr><th>Source</th><th>Provider</th><th>Settings</th><th>Token</th><th></th></tr></thead>
{/snippet}

{#snippet row(label: string, info: string | undefined, id: string | undefined, control: import('svelte').Snippet)}
  <div class="grid grid-cols-1 items-start gap-x-8 gap-y-2 py-4 sm:grid-cols-[14rem_minmax(0,1fr)]">
    <div class="flex h-8 items-center gap-1">
      <label for={id} class="text-sm text-fg">{label}</label>
      {#if info}<Info text={info} />{/if}
    </div>
    <div class="min-w-0">{@render control()}</div>
  </div>
{/snippet}

<PageHeader title="Settings" />

<div class="flex flex-col gap-9">
  <Section title="Host">
    <div class="divide-y divide-line/70 border-y border-line">
      {#snippet labelControl()}
        <form
          class="flex max-w-lg items-center gap-2"
          onsubmit={(e) => {
            e.preventDefault();
            saveLabel();
          }}
        >
          <input id="host-label" class="input" bind:value={label} placeholder={live.host?.hostname || 'hostname'} maxlength="64" autocomplete="off" />
          <Button type="submit" variant="primary" loading={labelSaving} disabled={!labelDirty}>Save</Button>
        </form>
      {/snippet}
      {@render row('Label', 'Stands in for the hostname everywhere', 'host-label', labelControl)}
      {#snippet hostnameControl()}
        <div class="flex h-8 items-center font-mono text-sm text-fg-muted">{live.host?.hostname ?? '–'}</div>
      {/snippet}
      {@render row('Hostname', undefined, undefined, hostnameControl)}
      {#snippet platformControl()}
        <div class="flex h-8 items-center font-mono text-sm text-fg-muted">{live.host ? `${live.host.os}/${live.host.arch}` : '–'}</div>
      {/snippet}
      {@render row('Platform', undefined, undefined, platformControl)}
    </div>
  </Section>

  <Section title="Connection">
    <div class="divide-y divide-line/70 border-y border-line">
      {#snippet daemonControl()}
        <div class="flex h-8 flex-wrap items-center gap-3 text-sm">
          <State tone={live.connected ? 'ok' : 'bad'} pulse={live.connected} label={live.connected ? 'Connected' : 'Disconnected'} />
          <span class="font-mono text-fg-muted">{baseUrl}</span>
          {#if live.needsToken}<span class="text-warn">token required</span>{/if}
          {#if live.error && !live.connected}<span class="truncate text-bad">{live.error}</span>{/if}
          <Button size="sm" variant="ghost" icon={Plug} loading={testing} onclick={test}>Test</Button>
        </div>
      {/snippet}
      {@render row('Daemon', undefined, undefined, daemonControl)}
      {#snippet tokenControl()}
        <form
          class="flex max-w-lg items-center gap-2"
          onsubmit={(e) => {
            e.preventDefault();
            save();
          }}
        >
          <div class="relative flex-1">
            <KeyRound size={13} class="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-fg-faint" />
            <input id="token" class="input pr-9 pl-8 font-mono" type={show ? 'text' : 'password'} bind:value autocomplete="off" placeholder="auth.token" />
            <button type="button" class="absolute top-1/2 right-1.5 -translate-y-1/2 rounded-sm p-1 text-fg-faint hover:text-fg" onclick={() => (show = !show)} aria-label={show ? 'Hide token' : 'Show token'}>
              {#if show}<EyeOff size={14} />{:else}<Eye size={14} />{/if}
            </button>
          </div>
          <Button type="submit" variant="primary" disabled={!tokenDirty}>Save</Button>
        </form>
      {/snippet}
      {@render row('API token', 'The daemon config sets auth.token. The browser keeps this copy.', 'token', tokenControl)}
    </div>
  </Section>

  <Section title="Sources" count={statuses.length || undefined}>
    {#snippet actions()}
      <Button size="sm" icon={Plus} onclick={add} disabled={!providers.length}>Add source</Button>
    {/snippet}
    {#if !cached.loaded}
      <table class="tbl">
        {@render head()}
        <tbody><SkeletonRows rows={3} cols={[{ w: 'w-40', sub: true }, 'w-20', 'w-32', 'w-16', { w: 'w-6', num: true }]} /></tbody>
      </table>
    {:else if cached.error && statuses.length === 0}
      <Empty compact title={cached.error} />
    {:else if statuses.length === 0}
      <Empty compact title="No sources" />
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          {@render head()}
          <tbody>
            {#each statuses as s (s.source?.id)}
              {@const c = s.capabilities}
              <tr>
                <td>
                  <div class="flex items-center gap-2">
                    <span class="text-fg">{sourceLabel(s)}</span>
                    {#if s.source?.seeded}<Tip text="Seeded by the daemon. Editable but not removable."><span class="text-xs text-fg-faint">built in</span></Tip>{/if}
                  </div>
                  <div class="font-mono text-xs text-fg-faint">{s.source?.id}</div>
                  {#if s.error}<div class="mt-1 max-w-md text-xs text-bad" title={s.error}>{s.error}</div>{/if}
                </td>
                <td>
                  <div class="text-fg">{c?.name}</div>
                  <div class="text-xs text-fg-faint">{c?.transports.join(' + ')}</div>
                </td>
                <td class="max-w-md">
                  {#if summary(s)}
                    <div class="truncate font-mono text-xs text-fg-muted" title={summary(s)}>{summary(s)}</div>
                  {:else}
                    <span class="text-xs text-fg-faint">defaults</span>
                  {/if}
                  {#if c?.endpoint || c?.webUrl}
                    <div class="mt-0.5 truncate text-xs text-fg-faint">{c.endpoint || c.webUrl}</div>
                  {/if}
                </td>
                <td>
                  {#if !c?.tokenEnv}
                    <span class="text-xs text-fg-faint">none</span>
                  {:else if c.tokenPresent}
                    <Tip text="{c.tokenEnv} is set for the daemon"><span class="inline-flex items-center gap-1.5 text-ok"><Check size={13} /><span class="font-mono text-xs">{c.tokenEnv}</span></span></Tip>
                  {:else if c.authRequired}
                    <Tip text="{c.tokenEnv} is not set. Downloads need it."><span class="inline-flex items-center gap-1.5 text-warn"><CircleAlert size={13} /><span class="font-mono text-xs">{c.tokenEnv}</span></span></Tip>
                  {:else}
                    <Tip text="{c.tokenEnv} is not set. Gated and private repositories need it."><span class="inline-flex items-center gap-1.5 text-fg-faint"><Circle size={13} /><span class="font-mono text-xs">{c.tokenEnv}</span></span></Tip>
                  {/if}
                </td>
                <td class="actions">
                  <span>
                    <IconButton size="sm" icon={Pencil} label="Edit" onclick={() => edit(s)} />
                    <Menu
                      size="sm"
                      items={[
                        { label: 'Open in the catalog', icon: ExternalLink, href: `/catalog?source=${s.source?.id ?? ''}` },
                        { label: '', separator: true },
                        { label: 'Remove', icon: Trash2, tone: 'bad', disabled: !!s.source?.seeded, onSelect: () => remove(s) }
                      ]}
                    />
                  </span>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Section>

  <Section title="Notifications">
    <div class="divide-y divide-line/70 border-y border-line">
      {#snippet notifyControl()}
        <div class="flex h-8 items-center"><Switch bind:checked={notify} onchange={toggleNotify} label="Desktop notifications" /></div>
      {/snippet}
      {@render row('Desktop notifications', 'From this browser whenever a watch or want turns something up', undefined, notifyControl)}
    </div>
  </Section>
</div>

<SourceDialog bind:open={dialogOpen} {providers} {editing} />
