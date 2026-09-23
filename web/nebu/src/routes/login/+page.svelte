<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { auth, login, setup, ssoLoginUrl } from '$lib/auth.svelte';
  import { connect, live } from '$lib/state.svelte';
  import { message, setToken } from '$lib/api';
  import { KeyRound, LogIn, UserPlus } from '@lucide/svelte';
  import Logo from '$lib/components/Logo.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import TextInput from '$lib/components/ui/TextInput.svelte';

  let username = $state('');
  let password = $state('');
  let confirm = $state('');
  let token = $state('');
  let useToken = $state(false);
  let busy = $state(false);
  let error = $state('');

  const next = $derived(safeNext(page.url.searchParams.get('next')));
  const setupMode = $derived(!!auth.local?.setup);

  // Stay inside the app after signing in.
  function safeNext(n: string | null): string {
    return n && n.startsWith('/') && !n.startsWith('//') ? n : '/';
  }

  // Leave once the daemon accepts this browser, or when it asks nothing of it.
  $effect(() => {
    if (live.connected || !auth.enabled) goto(next, { replaceState: true });
  });

  async function submit() {
    error = '';
    if (setupMode && password !== confirm) {
      error = 'Passwords do not match';
      return;
    }
    busy = true;
    try {
      if (setupMode) await setup(username, password);
      else await login(username, password);
      connect();
    } catch (err) {
      error = message(err);
    } finally {
      busy = false;
    }
  }

  function submitToken() {
    setToken(token.trim());
    connect();
  }

  // A full page load, the sign-in route belongs to the daemon rather than the app.
  function signInSso() {
    window.location.assign(ssoLoginUrl(next));
  }
</script>

<svelte:head>
  <title>Sign in · nebu</title>
</svelte:head>

<div class="flex min-h-screen items-center justify-center px-6 py-12">
  <div class="w-full max-w-sm">
    <div class="mb-8 flex items-center gap-2.5 text-fg">
      <Logo size={26} />
      <span class="font-mono text-[13px] font-semibold tracking-[0.3em]">NEBU</span>
    </div>
    {#if auth.local}
      <h1 class="text-lg font-semibold text-fg">{setupMode ? 'Create the first account' : 'Sign in'}</h1>
      <p class="mt-1 mb-6 text-sm text-fg-muted">
        {#if setupMode}No account exists yet. This one controls the daemon, and more can be added in Settings.{:else}Use a local account for this daemon.{/if}
      </p>
      <form
        class="flex flex-col gap-4"
        onsubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <Field label="Username" for="login-user">
          <TextInput id="login-user" bind:value={username} autocomplete="username" required />
        </Field>
        <Field label="Password" for="login-pass" description={setupMode ? 'At least 8 characters.' : undefined}>
          <TextInput id="login-pass" type="password" bind:value={password} autocomplete={setupMode ? 'new-password' : 'current-password'} required />
        </Field>
        {#if setupMode}
          <Field label="Confirm password" for="login-confirm">
            <TextInput id="login-confirm" type="password" bind:value={confirm} autocomplete="new-password" required />
          </Field>
        {/if}
        {#if error}<p class="text-sm text-bad" role="alert">{error}</p>{/if}
        <Button type="submit" variant="primary" size="lg" icon={setupMode ? UserPlus : LogIn} loading={busy} class="w-full">{setupMode ? 'Create account' : 'Sign in'}</Button>
      </form>
    {:else if auth.sso}
      <h1 class="text-lg font-semibold text-fg">Sign in</h1>
      <p class="mt-1 mb-6 text-sm text-fg-muted">This daemon signs browsers in through {auth.sso}.</p>
      <Button variant="primary" size="lg" icon={LogIn} class="w-full" onclick={signInSso}>Sign in with {auth.sso}</Button>
    {/if}
    {#if !setupMode}
      <div class="mt-8 border-t border-line pt-5">
        {#if useToken}
          <form
            class="flex flex-col gap-3"
            onsubmit={(e) => {
              e.preventDefault();
              submitToken();
            }}
          >
            <Field label="API token" for="login-token" description="An API token made in Settings, or the daemon's api.token file. Stored in this browser.">
              <TextInput id="login-token" mono type="password" bind:value={token}>
                {#snippet leading()}<KeyRound size={13} />{/snippet}
              </TextInput>
            </Field>
            <Button type="submit" variant="secondary" disabled={!token.trim()}>Use token</Button>
          </form>
        {:else}
          <button type="button" class="text-sm text-fg-muted underline underline-offset-2 hover:text-fg" onclick={() => (useToken = true)}>Use an API token instead</button>
        {/if}
        {#if live.needsToken && live.error && useToken}<p class="mt-2 text-sm text-warn">{live.error}</p>{/if}
      </div>
    {/if}
  </div>
</div>
