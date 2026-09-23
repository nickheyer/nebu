<script lang="ts">
  import { api, baseUrl, setToken, token } from '$lib/api';
  import { connect, live, updateSettings } from '$lib/state.svelte';
  import { auth, ssoLoginUrl, logout, login as localLogin, signInOffered } from '$lib/auth.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import type { ApiToken, User } from '$proto/auth_pb';
  import { when } from '$lib/format';
  import { KeyRound, Eye, EyeOff, Plug, LogIn, LogOut, UserPlus, Trash2 } from '@lucide/svelte';
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
    <div class="flex flex-col justify-center">
      <label for={id} class="flex h-8 items-center text-sm text-fg">{label}</label>
      {#if hint}<span class="-mt-1 text-xs text-fg-faint">{hint}</span>{/if}
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
          <TextInput id="host-label" class="flex-1" bind:value={label} empty={live.host?.hostname ?? ''} maxlength={64} />
          <Button type="submit" variant="primary" loading={labelSaving} disabled={!labelDirty}>Save</Button>
        </form>
      {/snippet}
      {@render row('Label', 'Shown instead of the hostname.', 'host-label', labelControl)}
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

  {#if auth.local}
    <Section title="Account">
      <div class="divide-y divide-line/70 border-y border-line">
        {#snippet accountControl()}
          <div class="flex min-h-8 flex-wrap items-center gap-3 text-sm">
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
          <div class="overflow-x-auto">
            <table class="tbl dense">
              <thead><tr><th>Username</th><th>Created</th><th>Password changed</th><th></th></tr></thead>
              <tbody>
                {#each users as u (u.id)}
                  <tr>
                    <td class="font-mono text-xs text-fg">{u.username}{#if me && u.username === me.name}<span class="ml-2 text-fg-faint">you</span>{/if}</td>
                    <td class="whitespace-nowrap text-xs text-fg-muted">{when(u.createdAt)}</td>
                    <td class="whitespace-nowrap text-xs text-fg-muted">{when(u.updatedAt)}</td>
                    <td class="actions"><span><IconButton icon={Trash2} label="Remove {u.username}" size="sm" disabled={(me && u.username === me.name) || users.length === 1} onclick={() => removeUser(u)} /></span></td>
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
          <div class="overflow-x-auto">
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
                    <td class="actions"><span><IconButton icon={Trash2} label="Revoke {t.name}" size="sm" onclick={() => revokeToken(t)} /></span></td>
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
        <div class="flex h-8 flex-wrap items-center gap-3 text-sm">
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
          <div class="flex min-h-8 flex-wrap items-center gap-3 text-sm">
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
        {@render row('API token', 'Stored in this browser. A token made in Settings while signed in, or the daemon\'s api.token file.', 'token', tokenControl)}
      {/if}
    </div>
  </Section>
</div>
