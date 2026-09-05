<script lang="ts">
  import { onMount } from 'svelte';
  import { Code, ConnectError } from '@connectrpc/connect';
  import { api, baseUrl, setToken, token, message } from '$lib/api';
  import { connect, live, cached, desktopNotify, setDesktopNotify } from '$lib/state.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { sourceLabel } from '$lib/catalog';
  import type { Provider, SourceStatus } from '$proto/source_pb';
  import { KeyRound, Save, Eye, EyeOff, Plug, Plus, Pencil, Trash2, Compass, ExternalLink } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Panel from '$lib/components/ui/Panel.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import Badge from '$lib/components/ui/Badge.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import Skeleton from '$lib/components/ui/Skeleton.svelte';
  import CheckCard from '$lib/components/ui/CheckCard.svelte';
  import SourceDialog from '$lib/components/SourceDialog.svelte';

  let value = $state(token());
  let show = $state(false);
  let notify = $state(desktopNotify());
  let testing = $state(false);
  let providers = $state<Provider[]>([]);
  let dialogOpen = $state(false);
  let editing = $state<SourceStatus | null>(null);

  const statuses = $derived(cached.sources);

  // Providers are compiled in, so one read per visit is enough
  onMount(() => {
    api.sources
      .listProviders({})
      .then((p) => (providers = p.providers))
      .catch((err) => fail(err, 'Could not list providers'));
  });

  async function toggleNotify(on: boolean) {
    notify = await setDesktopNotify(on);
    if (on && !notify) fail(new Error('the browser refused notification permission'), 'Desktop notifications stay off');
    else ok(notify ? 'Desktop notifications on' : 'Desktop notifications off', notify ? 'New findings and wanted models reach you even on another tab' : undefined);
  }

  function save() {
    setToken(value.trim());
    connect();
    ok(value.trim() ? 'Token saved' : 'Token cleared', 'Reconnecting to the daemon');
  }

  async function test() {
    testing = true;
    try {
      const r = await api.host.getProfile({});
      ok('Connected', `${r.profile?.hostname} answered`);
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
    const label = sourceLabel(s);
    const yes = await confirm({ title: `Remove ${label}?`, message: 'Models it pulled stay in the store under its id. Watches on it keep their findings but cannot check again.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.sources.deleteSource({ id: s.source?.id ?? '' });
      ok(`Removed ${label}`);
    } catch (err) {
      if (!(err instanceof ConnectError && err.code === Code.FailedPrecondition)) {
        fail(err, 'Remove failed');
        return;
      }
      // The daemon names the watches and wants on the source, dropping them is the person's call
      const force = await confirm({ title: `Drop what names ${label}?`, message: `${message(err)}. Its watches are removed and wants narrowed to it look everywhere instead.`, action: 'Remove and drop references', tone: 'bad' });
      if (!force) return;
      try {
        await api.sources.deleteSource({ id: s.source?.id ?? '', force: true });
        ok(`Removed ${label}`, 'Its watches went with it and its wants look everywhere');
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

  function auth(s: SourceStatus): { label: string; tone: 'ok' | 'warn' | 'neutral' } | null {
    const c = s.capabilities;
    if (!c?.tokenEnv) return null;
    if (c.tokenPresent) return { label: `${c.tokenEnv} set`, tone: 'ok' };
    if (c.authRequired) return { label: `${c.tokenEnv} needed`, tone: 'warn' };
    return { label: `${c.tokenEnv} unset`, tone: 'neutral' };
  }
</script>

<PageHeader title="Settings" description="Where models come from, and this browser's connection to the daemon" />

<div class="flex flex-col gap-4">
  <Panel title="Sources" description="Each source is one configured instance of a provider. Seeded defaults can be edited but not removed." flush>
    {#snippet actions()}
      <Button size="sm" variant="primary" icon={Plus} onclick={add} disabled={!providers.length}>Add source</Button>
    {/snippet}
    {#if !cached.loaded}
      <Skeleton rows={4} class="p-4" />
    {:else if cached.error && statuses.length === 0}
      <Empty compact icon={Compass} title="Sources unavailable" description={cached.error} />
    {:else if statuses.length === 0}
      <Empty compact icon={Compass} title="No sources" description="Add one to start browsing a catalog.">
        <Button size="sm" variant="primary" icon={Plus} onclick={add}>Add source</Button>
      </Empty>
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          <thead><tr><th>source</th><th>provider</th><th>settings</th><th>auth</th><th></th></tr></thead>
          <tbody>
            {#each statuses as s (s.source?.id)}
              {@const a = auth(s)}
              <tr>
                <td>
                  <div class="flex items-center gap-2">
                    <span class="font-medium text-fg">{sourceLabel(s)}</span>
                    {#if s.source?.seeded}<Badge size="xs" label="seeded" />{/if}
                  </div>
                  <div class="font-mono text-[11px] text-fg-faint">{s.source?.id}</div>
                  {#if s.error}<div class="mt-1 max-w-md text-[11px] text-bad" title={s.error}>{s.error}</div>{/if}
                </td>
                <td class="text-xs">
                  <div class="text-fg">{s.capabilities?.name}</div>
                  <div class="text-fg-faint">{s.capabilities?.transports.join(' + ')}</div>
                </td>
                <td class="max-w-md">
                  {#if summary(s)}
                    <div class="truncate font-mono text-[11px] text-fg-muted" title={summary(s)}>{summary(s)}</div>
                  {:else}
                    <span class="text-xs text-fg-faint">provider defaults</span>
                  {/if}
                  {#if s.capabilities?.endpoint || s.capabilities?.webUrl}
                    <div class="mt-0.5 truncate text-[11px] text-fg-faint">{s.capabilities.endpoint || s.capabilities.webUrl}</div>
                  {/if}
                </td>
                <td>{#if a}<Badge size="xs" tone={a.tone} label={a.label} />{:else}<span class="text-xs text-fg-faint">none</span>{/if}</td>
                <td class="text-right">
                  <Menu
                    items={[
                      { label: 'Edit', icon: Pencil, onSelect: () => edit(s) },
                      { label: 'Open in catalog', icon: ExternalLink, href: `/catalog?source=${s.source?.id ?? ''}` },
                      { label: '', separator: true },
                      { label: 'Remove', icon: Trash2, tone: 'bad', disabled: !!s.source?.seeded, onSelect: () => remove(s) }
                    ]}
                  />
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Panel>

  <div class="grid grid-cols-1 gap-4 lg:grid-cols-2">
    <Panel title="API token" description="Required when the daemon sets auth.token. Kept in this browser only.">
      <form
        class="flex flex-col gap-3"
        onsubmit={(e) => {
          e.preventDefault();
          save();
        }}
      >
        <Field label="Bearer token" for="token">
          <div class="relative">
            <KeyRound size={14} class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-fg-faint" />
            <input id="token" class="input pr-10 pl-9 font-mono" type={show ? 'text' : 'password'} bind:value autocomplete="off" placeholder="paste the daemon's auth.token" />
            <button type="button" class="absolute top-1/2 right-2 -translate-y-1/2 rounded p-1 text-fg-faint hover:text-fg" onclick={() => (show = !show)} aria-label={show ? 'Hide token' : 'Show token'}>
              {#if show}<EyeOff size={14} />{:else}<Eye size={14} />{/if}
            </button>
          </div>
        </Field>
        <div class="flex gap-2">
          <Button type="submit" variant="primary" icon={Save}>Save and reconnect</Button>
          <Button type="button" variant="outline" icon={Plug} loading={testing} onclick={test}>Test connection</Button>
        </div>
      </form>
    </Panel>

    <Panel title="Connection">
      <div class="flex flex-col gap-3">
        <div class="flex items-center gap-2">
          <Badge tone={live.connected ? 'ok' : 'bad'} dot pulse={live.connected} label={live.connected ? 'live updates connected' : 'disconnected'} />
          {#if live.needsToken}<Badge tone="warn" label="token required" />{/if}
        </div>
        <Kv
          mono
          items={[
            ['api', baseUrl],
            ['host', live.host?.hostname],
            ['platform', live.host ? `${live.host.os}/${live.host.arch}` : undefined],
            ['stream error', live.error || undefined]
          ]}
        />
        <p class="text-xs leading-5 text-fg-faint">The page holds one server stream from the daemon and renders everything from it. When it drops, it reconnects with backoff and replays a snapshot.</p>
      </div>
    </Panel>
  </div>
  <Panel title="Notifications" description="Findings always appear as toasts while a page is open, and on the monitor page until acknowledged">
    <CheckCard bind:checked={notify} onchange={toggleNotify} title="Desktop notifications in this browser" description="A system notification for every new finding and every wanted model that turns up. Webhooks for other systems are set in the daemon config under notify." />
  </Panel>
</div>

<SourceDialog bind:open={dialogOpen} {providers} {editing} />
