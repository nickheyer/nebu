<script lang="ts">
  import { onMount } from 'svelte';
  import { Code, ConnectError } from '@connectrpc/connect';
  import { api, baseUrl, setToken, token, message } from '$lib/api';
  import { connect, live, cached, desktopNotify, setDesktopNotify, updateSettings } from '$lib/state.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { sourceLabel } from '$lib/catalog';
  import type { Provider, SourceStatus } from '$proto/source_pb';
  import { KeyRound, Save, Eye, EyeOff, Plug, Plus, Pencil, Trash2, ExternalLink, Check, Circle, CircleAlert } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import Pill from '$lib/components/ui/Pill.svelte';
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
    else ok(notify ? 'Desktop notifications on' : 'Desktop notifications off');
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
      if (!(err instanceof ConnectError && err.code === Code.FailedPrecondition)) {
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

<PageHeader title="Settings" />

<div class="flex flex-col gap-6">
  <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
    <Card title="This host" description="The label stands in for the hostname everywhere">
      <form
        class="flex flex-col gap-4"
        onsubmit={(e) => {
          e.preventDefault();
          saveLabel();
        }}
      >
        <Field label="Label" for="host-label">
          <input id="host-label" class="input" bind:value={label} placeholder={live.host?.hostname || 'hostname'} maxlength="64" autocomplete="off" />
        </Field>
        <Kv
          mono
          items={[
            ['hostname', live.host?.hostname],
            ['platform', live.host ? `${live.host.os}/${live.host.arch}` : undefined]
          ]}
        />
        <div class="flex gap-2">
          <Button type="submit" variant="primary" icon={Save} loading={labelSaving} disabled={!labelDirty}>Save</Button>
        </div>
      </form>
    </Card>

    <Card title="Connection" description="The token is kept in this browser only">
      <form
        class="flex flex-col gap-4"
        onsubmit={(e) => {
          e.preventDefault();
          save();
        }}
      >
        <div class="flex flex-wrap items-center gap-2 text-sm">
          <Pill tone={live.connected ? 'ok' : 'bad'} dot pulse={live.connected} label={live.connected ? 'Connected' : 'Disconnected'} />
          <span class="font-mono text-fg-muted">{baseUrl}</span>
          {#if live.needsToken}<span class="text-warn">token required</span>{/if}
          {#if live.error && !live.connected}<span class="truncate text-bad">{live.error}</span>{/if}
        </div>
        <Field label="API token" for="token" hint="Set as auth.token in the daemon's config">
          <div class="relative">
            <KeyRound size={15} class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-fg-faint" />
            <input id="token" class="input pr-10 pl-9 font-mono" type={show ? 'text' : 'password'} bind:value autocomplete="off" placeholder="auth.token" />
            <button type="button" class="absolute top-1/2 right-2 -translate-y-1/2 rounded-md p-1 text-fg-faint hover:text-fg" onclick={() => (show = !show)} aria-label={show ? 'Hide token' : 'Show token'}>
              {#if show}<EyeOff size={15} />{:else}<Eye size={15} />{/if}
            </button>
          </div>
        </Field>
        <div class="flex gap-2">
          <Button type="submit" variant="primary" icon={Save}>Save</Button>
          <Button type="button" icon={Plug} loading={testing} onclick={test}>Test</Button>
        </div>
      </form>
    </Card>
  </div>

  <Card title="Sources" flush>
    {#snippet actions()}
      <Button size="sm" variant="primary" icon={Plus} onclick={add} disabled={!providers.length}>Add source</Button>
    {/snippet}
    {#if !cached.loaded}
      <table class="tbl">
        {@render head()}
        <tbody><SkeletonRows rows={3} cols={[{ w: 'w-40', sub: true }, 'w-20', 'w-32', 'w-16', { w: 'w-6', num: true }]} /></tbody>
      </table>
    {:else if cached.error && statuses.length === 0}
      <Empty compact title="Sources unavailable" description={cached.error} />
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
                    <span class="font-medium text-fg">{sourceLabel(s)}</span>
                    {#if s.source?.seeded}<Tip text="Created by the daemon. It can be edited but not removed"><Pill label="built in" /></Tip>{/if}
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
                    <span class="text-fg-faint">defaults</span>
                  {/if}
                  {#if c?.endpoint || c?.webUrl}
                    <div class="mt-0.5 truncate text-xs text-fg-faint">{c.endpoint || c.webUrl}</div>
                  {/if}
                </td>
                <td>
                  {#if !c?.tokenEnv}
                    <span class="text-fg-faint">none</span>
                  {:else if c.tokenPresent}
                    <Tip text="{c.tokenEnv} is set for the daemon"><span class="inline-flex items-center gap-1.5 text-ok"><Check size={14} /><span class="font-mono text-xs">{c.tokenEnv}</span></span></Tip>
                  {:else if c.authRequired}
                    <Tip text="{c.tokenEnv} is not set. Downloads need it"><span class="inline-flex items-center gap-1.5 text-warn"><CircleAlert size={14} /><span class="font-mono text-xs">{c.tokenEnv}</span></span></Tip>
                  {:else}
                    <Tip text="{c.tokenEnv} is not set. Gated and private repositories need it"><span class="inline-flex items-center gap-1.5 text-fg-faint"><Circle size={14} /><span class="font-mono text-xs">{c.tokenEnv}</span></span></Tip>
                  {/if}
                </td>
                <td class="actions">
                  <Menu
                    size="sm"
                    items={[
                      { label: 'Edit', icon: Pencil, onSelect: () => edit(s) },
                      { label: 'Open in the catalog', icon: ExternalLink, href: `/catalog?source=${s.source?.id ?? ''}` },
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
  </Card>

  <Card title="Notifications">
    <Switch bind:checked={notify} onchange={toggleNotify} title="Desktop notifications" hint="A notification from this browser whenever a watch or want turns something up" />
  </Card>
</div>

<SourceDialog bind:open={dialogOpen} {providers} {editing} />
