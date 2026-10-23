<script lang="ts">
  import { api, baseUrl, setToken, token } from '$lib/api';
  import { connect, live, updateSettings } from '$lib/state.svelte';
  import { auth, ssoLoginUrl, logout, login as localLogin, signInOffered } from '$lib/auth.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import type { ApiToken, User } from '$proto/auth_pb';
  import type { ToolServer } from '$proto/settings_pb';
  import { ToolServerState } from '$proto/tools_pb';
  import { when } from '$lib/format';
  import { kindLabel, stateLabel, stateTone, toolServers, toolServerFields, toolServerFrom, toolServerProblem, type ToolServerFields } from '$lib/tools';
  import { KeyRound, Eye, EyeOff, Plug, LogIn, LogOut, UserPlus, Trash2, Plus, Pencil, Wrench } from '@lucide/svelte';
  import Dialog from '$lib/components/ui/Dialog.svelte';
  import Switch from '$lib/components/ui/Switch.svelte';
  import ToolServerForm from '$lib/components/ToolServerForm.svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Copy from '$lib/components/ui/Copy.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import State from '$lib/components/ui/State.svelte';
  import TextInput from '$lib/components/ui/TextInput.svelte';

  let value = $state(token());
  let show = $state(false);
  let testing = $state(false);
  let signingOut = $state(false);
  let label = $state('');
  let labelSaving = $state(false);
  let labelSeeded = false;

  // Tool servers: the list in Settings, and the one open in the editor
  let toolOpen = $state(false);
  let toolFields = $state<ToolServerFields>(toolServerFields());
  let toolEditing = $state('');
  let toolSaving = $state(false);

  // Password change
  let current = $state('');
  let fresh = $state('');
  let again = $state('');
  let changing = $state(false);

  // Accounts
  let users = $state<User[]>([]);
  let usersLoaded = $state(false);
  let newName = $state('');
  let newPass = $state('');
  let adding = $state(false);

  // API tokens of the signed-in account
  let tokens = $state<ApiToken[]>([]);
  let tokensLoaded = $state(false);
  let tokenName = $state('');
  let creating = $state(false);
  let revealed = $state<Record<string, boolean>>({});

  const labelDirty = $derived(label.trim() !== (live.settings?.hostLabel ?? ''));
  const servers = $derived(live.settings?.toolServers ?? []);
  const statuses = $derived(toolServers());
  const toolProblem = $derived(toolServerProblem(toolFields, servers.filter((s) => s.id !== toolEditing).map((s) => s.name)));
  const mcpUrl = $derived(`${baseUrl}/mcp`);
  const tokenDirty = $derived(value.trim() !== token());
  const me = $derived(auth.user?.provider === 'local' ? auth.user : null);

  $effect(() => {
    if (live.settings && !labelSeeded) {
      label = live.settings.hostLabel;
      labelSeeded = true;
    }
  });

  $effect(() => {
    if (auth.local && live.connected && !usersLoaded) loadUsers();
  });

  $effect(() => {
    if (auth.user && live.connected && !tokensLoaded) loadTokens();
  });

  async function saveLabel() {
    labelSaving = true;
    if (await updateSettings({ hostLabel: label.trim() })) ok(label.trim() ? `Label set to ${label.trim()}` : 'Label cleared');
    labelSaving = false;
  }

  function addServer() {
    toolFields = toolServerFields();
    toolEditing = '';
    toolOpen = true;
  }

  function editServer(s: ToolServer) {
    toolFields = toolServerFields(s);
    toolEditing = s.id;
    toolOpen = true;
  }

  async function saveServers(next: ToolServer[], done: string): Promise<boolean> {
    toolSaving = true;
    const saved = await updateSettings({ toolServers: next });
    toolSaving = false;
    if (saved) ok(done);
    return saved;
  }

  async function saveServer() {
    const server = toolServerFrom(toolFields);
    const next = toolEditing ? servers.map((s) => (s.id === toolEditing ? server : s)) : [...servers, server];
    if (await saveServers(next, toolEditing ? `Saved ${server.name}` : `Added ${server.name}`)) toolOpen = false;
  }

  async function toggleServer(s: ToolServer, enabled: boolean) {
    await saveServers(servers.map((x) => (x.id === s.id ? ({ ...x, enabled } as ToolServer) : x)), enabled ? `${s.name} turned on` : `${s.name} turned off`);
  }

  async function removeServer(s: ToolServer) {
    if (!(await confirm({ title: `Remove ${s.name}?`, message: 'Routes and personas that attach it must let go first.', action: 'Remove', tone: 'bad' }))) return;
    await saveServers(servers.filter((x) => x.id !== s.id), `Removed ${s.name}`);
  }

  function save() {
    setToken(value.trim());
    connect();
    ok(value.trim() ? 'Token saved' : 'Token cleared');
  }

  // A full page load, the sign-in route belongs to the daemon rather than the app.
  function signInSso() {
    window.location.assign(ssoLoginUrl('/settings'));
  }

  async function signOut() {
    signingOut = true;
    try {
      await logout();
      ok('Signed out');
      connect();
    } catch (err) {
      fail(err, 'Sign out failed');
    } finally {
      signingOut = false;
    }
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

  async function loadUsers() {
    try {
      const r = await api.auth.listUsers({});
      users = r.users;
    } catch (err) {
      fail(err, 'Could not list accounts');
    } finally {
      usersLoaded = true;
    }
  }

  async function changePassword() {
    const user = me;
    if (!user) return;
    if (fresh !== again) {
      fail(new Error('The new passwords do not match'), 'Password not changed');
      return;
    }
    changing = true;
    try {
      await api.auth.setPassword({ username: user.name, password: fresh, currentPassword: current });
      // The change ended the old session, sign in again with the new password
      await localLogin(user.name, fresh);
      current = fresh = again = '';
      ok('Password changed');
    } catch (err) {
      fail(err, 'Password not changed');
    } finally {
      changing = false;
    }
  }

  async function addUser() {
    adding = true;
    try {
      const r = await api.auth.createUser({ username: newName.trim(), password: newPass });
      ok(`Added ${r.user?.username}`);
      newName = newPass = '';
      await loadUsers();
    } catch (err) {
      fail(err, 'Account not added');
    } finally {
      adding = false;
    }
  }

  async function removeUser(u: User) {
    if (!(await confirm({ title: `Remove ${u.username}?`, message: 'Its sessions end and its API tokens stop working now.', action: 'Remove', tone: 'bad' }))) return;
    try {
      await api.auth.deleteUser({ username: u.username });
      ok(`Removed ${u.username}`);
      await loadUsers();
    } catch (err) {
      fail(err, 'Account not removed');
    }
  }

  async function loadTokens() {
    try {
      const r = await api.auth.listTokens({});
      tokens = r.tokens;
    } catch (err) {
      fail(err, 'Could not list API tokens');
    } finally {
      tokensLoaded = true;
    }
  }

  async function createToken() {
    creating = true;
    try {
      const r = await api.auth.createToken({ name: tokenName.trim() });
      if (r.token) revealed[r.token.id] = true;
      ok(`Made ${r.token?.name}`);
      tokenName = '';
      await loadTokens();
    } catch (err) {
      fail(err, 'Token not made');
    } finally {
      creating = false;
    }
  }

  async function revokeToken(t: ApiToken) {
    if (!(await confirm({ title: `Revoke ${t.name}?`, message: 'Clients sending it are refused from now on.', action: 'Revoke', tone: 'bad' }))) return;
    try {
      await api.auth.deleteToken({ id: t.id });
      ok(`Revoked ${t.name}`);
      await loadTokens();
    } catch (err) {
      fail(err, 'Token not revoked');
    }
  }
</script>

{#snippet row(label: string, hint: string | undefined, id: string | undefined, control: import('svelte').Snippet)}
  <div class="grid grid-cols-1 items-start gap-x-8 gap-y-2 py-4 sm:grid-cols-[14rem_minmax(0,1fr)]">
    <div class="flex flex-col">
      <label for={id} class="flex h-9 items-center text-sm text-fg">{label}</label>
      {#if hint}<span class="-mt-1.5 text-xs leading-5 text-fg-muted">{hint}</span>{/if}
    </div>
    <div class="min-w-0">{@render control()}</div>
  </div>
{/snippet}

<PageHeader title="Settings" />

<div class="flex flex-col gap-10">
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
          <TextInput id="host-label" class="flex-1" bind:value={label} empty={live.host?.hostname ?? ''} maxlength={64} />
          <Button type="submit" variant="primary" loading={labelSaving} disabled={!labelDirty}>Save</Button>
        </form>
      {/snippet}
      {@render row('Label', 'Shown instead of the hostname.', 'host-label', labelControl)}
      {#snippet hostnameControl()}
        <div class="flex h-9 items-center font-mono text-sm text-fg-muted">{live.host?.hostname ?? '–'}</div>
      {/snippet}
      {@render row('Hostname', undefined, undefined, hostnameControl)}
      {#snippet platformControl()}
        <div class="flex h-9 items-center font-mono text-sm text-fg-muted">{live.host ? `${live.host.os}/${live.host.arch}` : '–'}</div>
      {/snippet}
      {@render row('Platform', undefined, undefined, platformControl)}
    </div>
  </Section>

  <Section title="Tool servers" meta={servers.length ? `${servers.length} ${servers.length === 1 ? 'server' : 'servers'}` : ''}>
    {#snippet actions()}
      <Button size="sm" icon={Plus} onclick={addServer}>Add server</Button>
    {/snippet}
    <div class="flex flex-col gap-4">
      <p class="text-sm text-fg-muted">Web search, web fetch, and MCP servers the gateway runs for models, attached to routes, personas, or chats.</p>
      {#if !servers.length}
        <p class="text-sm text-fg-faint">None yet.</p>
      {:else}
        <div class="tbl-wrap">
          <table class="tbl dense">
            <thead><tr><th>Name</th><th>Kind</th><th>State</th><th>Tools</th><th>On</th><th></th></tr></thead>
            <tbody>
              {#each servers as s (s.id)}
                {@const st = statuses.find((x) => x.server?.name === s.name)}
                <tr>
                  <td class="font-mono text-xs text-fg">{s.name}</td>
                  <td class="text-xs text-fg-muted">{kindLabel(s.kind)}</td>
                  <td>
                    {#if st}<State tone={stateTone(st.state)} label={stateLabel(st.state)} pulse={st.state === ToolServerState.CONNECTING} />{:else}<State tone="neutral" label="Starting" />{/if}
                    {#if st?.error}<div class="mt-1 max-w-xs truncate text-xs text-bad" title={st.error}>{st.error}</div>{/if}
                  </td>
                  <td class="max-w-sm truncate font-mono text-xs text-fg-muted" title={st?.tools.map((t) => t.name).join(', ')}>{st?.tools.length ? st.tools.map((t) => t.name + (t.readOnly ? '' : '*')).join(', ') : '–'}</td>
                  <td><Switch checked={s.enabled} label="Enabled" onchange={(on) => toggleServer(s, on)} /></td>
                  <td class="actions"><span class="flex gap-1"><IconButton icon={Pencil} label="Edit {s.name}" size="xs" onclick={() => editServer(s)} /><IconButton icon={Trash2} label="Remove {s.name}" size="xs" onclick={() => removeServer(s)} /></span></td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        <p class="text-xs text-fg-faint">Tools marked * change things and are offered only where mutating tools are allowed.</p>
      {/if}
    </div>
  </Section>

  <Section title="MCP">
    <div class="divide-y divide-line/70 border-y border-line">
      {#snippet mcpControl()}
        <div class="flex min-h-9 flex-wrap items-center gap-2 text-sm">
          <Wrench size={13} class="text-fg-faint" />
          <span class="font-mono text-fg">{mcpUrl}</span>
          <Copy text={mcpUrl} label="Copy the MCP URL" size={13} />
        </div>
      {/snippet}
      {@render row('Endpoint', 'Add this URL to Claude Code, Claude Desktop, or another MCP client as a streamable HTTP server, with an API token as the bearer token.', undefined, mcpControl)}
    </div>
  </Section>

  {#if auth.local}
    <Section title="Account">
      <div class="divide-y divide-line/70 border-y border-line">
        {#snippet accountControl()}
          <div class="flex min-h-9 flex-wrap items-center gap-3 text-sm">
            {#if me}
              <State tone="ok" label={me.name} />
              <span class="text-xs text-fg-faint">Session ends {new Date(me.expires).toLocaleString()}</span>
              <Button size="sm" variant="ghost" icon={LogOut} loading={signingOut} onclick={signOut}>Sign out</Button>
            {:else}
              <span class="text-fg-muted">Not signed in with an account</span>
              <Button size="sm" variant="primary" icon={LogIn} href="/login?next=%2Fsettings">Sign in</Button>
            {/if}
          </div>
        {/snippet}
        {@render row('Signed in as', undefined, undefined, accountControl)}
        {#if me}
          {#snippet passwordControl()}
            <form
              class="flex max-w-lg flex-col gap-2"
              onsubmit={(e) => {
                e.preventDefault();
                changePassword();
              }}
            >
              <TextInput id="pw-current" type="password" bind:value={current} empty="Current password" autocomplete="current-password" required />
              <TextInput id="pw-new" type="password" bind:value={fresh} empty="New password, at least 8 characters" autocomplete="new-password" required />
              <div class="flex items-center gap-2">
                <TextInput id="pw-again" class="flex-1" type="password" bind:value={again} empty="Confirm new password" autocomplete="new-password" required />
                <Button type="submit" variant="primary" loading={changing} disabled={!current || !fresh || !again}>Change</Button>
              </div>
            </form>
          {/snippet}
          {@render row('Password', 'Other sessions of this account end when it changes.', 'pw-current', passwordControl)}
        {/if}
      </div>
    </Section>

    <Section title="Accounts" meta={usersLoaded ? `${users.length} ${users.length === 1 ? 'account' : 'accounts'}` : ''}>
      <div class="flex flex-col gap-4">
        {#if !usersLoaded}
          <div class="skeleton h-10" aria-busy="true"></div>
        {:else}
          <div class="tbl-wrap">
            <table class="tbl dense">
              <thead><tr><th>Username</th><th>Created</th><th>Password changed</th><th></th></tr></thead>
              <tbody>
                {#each users as u (u.id)}
                  <tr>
                    <td class="font-mono text-xs text-fg">{u.username}{#if me && u.username === me.name}<span class="ml-2 text-fg-faint">you</span>{/if}</td>
                    <td class="whitespace-nowrap text-xs text-fg-muted">{when(u.createdAt)}</td>
                    <td class="whitespace-nowrap text-xs text-fg-muted">{when(u.updatedAt)}</td>
                    <td class="actions"><span><IconButton icon={Trash2} label="Remove {u.username}" size="xs" disabled={(me && u.username === me.name) || users.length === 1} onclick={() => removeUser(u)} /></span></td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
        <form
          class="flex max-w-lg flex-wrap items-center gap-2"
          onsubmit={(e) => {
            e.preventDefault();
            addUser();
          }}
        >
          <TextInput id="user-name" class="flex-1" bind:value={newName} empty="Username" autocomplete="off" required />
          <TextInput id="user-pass" class="flex-1" type="password" bind:value={newPass} empty="Password" autocomplete="new-password" required />
          <Button type="submit" variant="secondary" icon={UserPlus} loading={adding} disabled={!newName.trim() || !newPass}>Add account</Button>
        </form>
      </div>
    </Section>
  {/if}

  {#if auth.user}
    <Section title="API tokens" meta={tokensLoaded ? `${tokens.length} ${tokens.length === 1 ? 'token' : 'tokens'}` : ''}>
      <div class="flex flex-col gap-4">
        <p class="text-sm text-fg-muted">
          Clients send one as <code class="font-mono text-xs text-fg">Authorization: Bearer</code> to the API and the gateway, and act as {auth.user.name || auth.user.email || auth.user.subject}.
        </p>
        {#if !tokensLoaded}
          <div class="skeleton h-10" aria-busy="true"></div>
        {:else if tokens.length}
          <div class="tbl-wrap">
            <table class="tbl dense">
              <thead><tr><th>Name</th><th>Token</th><th>Created</th><th>Last used</th><th></th></tr></thead>
              <tbody>
                {#each tokens as t (t.id)}
                  <tr>
                    <td class="text-fg">{t.name}</td>
                    <td class="font-mono text-xs text-fg-muted">
                      <span class="inline-flex items-center gap-1">
                        <span class="select-all {revealed[t.id] ? 'text-fg' : ''}">{revealed[t.id] ? t.secret : '•'.repeat(24)}</span>
                        <button type="button" class="rounded-sm p-1 text-fg-faint hover:text-fg" onclick={() => (revealed[t.id] = !revealed[t.id])} aria-label={revealed[t.id] ? `Hide ${t.name}` : `Show ${t.name}`}>
                          {#if revealed[t.id]}<EyeOff size={13} />{:else}<Eye size={13} />{/if}
                        </button>
                        <Copy text={t.secret} label="Copy {t.name}" size={13} />
                      </span>
                    </td>
                    <td class="whitespace-nowrap text-xs text-fg-muted">{when(t.createdAt)}</td>
                    <td class="whitespace-nowrap text-xs text-fg-muted">{t.lastUsedAt ? when(t.lastUsedAt) : 'Never'}</td>
                    <td class="actions"><span><IconButton icon={Trash2} label="Revoke {t.name}" size="xs" onclick={() => revokeToken(t)} /></span></td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
        <form
          class="flex max-w-lg flex-wrap items-center gap-2"
          onsubmit={(e) => {
            e.preventDefault();
            createToken();
          }}
        >
          <TextInput id="token-name" class="flex-1" bind:value={tokenName} empty="Name, such as laptop or ci" autocomplete="off" required />
          <Button type="submit" variant="secondary" icon={KeyRound} loading={creating} disabled={!tokenName.trim()}>Generate token</Button>
        </form>
      </div>
    </Section>
  {/if}

  <Section title="Connection">
    <div class="divide-y divide-line/70 border-y border-line">
      {#snippet daemonControl()}
        <div class="flex min-h-9 flex-wrap items-center gap-3 text-sm">
          <State tone={live.connected ? 'ok' : 'bad'} pulse={live.connected} label={live.connected ? 'Connected' : 'Disconnected'} />
          <span class="font-mono text-fg-muted">{baseUrl}</span>
          {#if live.needsToken}<span class="text-warn">{signInOffered() ? 'Sign in required' : 'Token required'}</span>{/if}
          {#if live.error && !live.connected}<span class="truncate text-bad">{live.error}</span>{/if}
          <Button size="sm" variant="ghost" icon={Plug} loading={testing} onclick={test}>Test</Button>
        </div>
      {/snippet}
      {@render row('Daemon', undefined, undefined, daemonControl)}
      {#if auth.sso}
        {#snippet ssoControl()}
          <div class="flex min-h-9 flex-wrap items-center gap-3 text-sm">
            {#if auth.user?.provider === 'oidc'}
              <State tone="ok" label={auth.user.name || auth.user.email || auth.user.subject} />
              {#if auth.user.email && auth.user.email !== auth.user.name}<span class="font-mono text-fg-muted">{auth.user.email}</span>{/if}
              <Button size="sm" variant="ghost" icon={LogOut} loading={signingOut} onclick={signOut}>Sign out</Button>
            {:else}
              <Button size="sm" variant="primary" icon={LogIn} onclick={signInSso}>Sign in with {auth.sso}</Button>
            {/if}
          </div>
        {/snippet}
        {@render row('Single sign-on', auth.user?.provider === 'oidc' ? `Session ends ${new Date(auth.user.expires).toLocaleString()}.` : `Signs this browser in through ${auth.sso} instead of an API token.`, undefined, ssoControl)}
      {/if}
      {#if !auth.user}
        {#snippet tokenControl()}
          <form
            class="flex max-w-lg items-center gap-2"
            onsubmit={(e) => {
              e.preventDefault();
              save();
            }}
          >
            <div class="relative flex-1">
              <TextInput id="token" mono type={show ? 'text' : 'password'} bind:value inputClass="pr-9">
                {#snippet leading()}<KeyRound size={13} />{/snippet}
              </TextInput>
              <button type="button" class="absolute top-1/2 right-1.5 -translate-y-1/2 rounded-sm p-1 text-fg-faint hover:text-fg" onclick={() => (show = !show)} aria-label={show ? 'Hide token' : 'Show token'}>
                {#if show}<EyeOff size={14} />{:else}<Eye size={14} />{/if}
              </button>
            </div>
            <Button type="submit" variant="primary" disabled={!tokenDirty}>Save</Button>
          </form>
        {/snippet}
        {@render row('API token', 'Kept in this browser. A token from Settings, or the daemon\'s api.token file.', 'token', tokenControl)}
      {/if}
    </div>
  </Section>
</div>

<Dialog bind:open={toolOpen} title={toolEditing ? 'Edit tool server' : 'Add tool server'} size="lg">
  <form
    onsubmit={(e) => {
      e.preventDefault();
      if (!toolProblem && !toolSaving) saveServer();
    }}
  >
    <ToolServerForm bind:fields={toolFields} problem={toolProblem} />
    <button type="submit" class="hidden" aria-hidden="true" tabindex="-1"></button>
  </form>
  {#snippet footer()}
    <span class="ml-auto flex gap-2">
      <Button variant="ghost" onclick={() => (toolOpen = false)}>Cancel</Button>
      <Button variant="primary" loading={toolSaving} disabled={!!toolProblem} onclick={saveServer}>{toolEditing ? 'Save' : 'Add'}</Button>
    </span>
  {/snippet}
</Dialog>
