<script lang="ts">
  import { api, baseUrl, setToken, token } from '$lib/api';
  import { connect, live, updateSettings } from '$lib/state.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import { KeyRound, Eye, EyeOff, Plug } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import State from '$lib/components/ui/State.svelte';
  import TextInput from '$lib/components/ui/TextInput.svelte';

  let value = $state(token());
  let show = $state(false);
  let testing = $state(false);
  let label = $state('');
  let labelSaving = $state(false);
  let labelSeeded = false;

  const labelDirty = $derived(label.trim() !== (live.settings?.hostLabel ?? ''));
  const tokenDirty = $derived(value.trim() !== token());

  // The label field starts from the daemon's value once it arrives
  $effect(() => {
    if (live.settings && !labelSeeded) {
      label = live.settings.hostLabel;
      labelSeeded = true;
    }
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

  <Section title="Connection">
    <div class="divide-y divide-line/70 border-y border-line">
      {#snippet daemonControl()}
        <div class="flex h-8 flex-wrap items-center gap-3 text-sm">
          <State tone={live.connected ? 'ok' : 'bad'} pulse={live.connected} label={live.connected ? 'Connected' : 'Disconnected'} />
          <span class="font-mono text-fg-muted">{baseUrl}</span>
          {#if live.needsToken}<span class="text-warn">Token required</span>{/if}
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
      {@render row('API token', 'Stored in this browser. Must match auth.token in the daemon config.', 'token', tokenControl)}
    </div>
  </Section>
</div>
