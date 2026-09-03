<script lang="ts">
  import { api, baseUrl, setToken, token } from '$lib/api';
  import { connect, live } from '$lib/state.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import { KeyRound, Save, Eye, EyeOff, Plug } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Panel from '$lib/components/ui/Panel.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import Badge from '$lib/components/ui/Badge.svelte';

  let value = $state(token());
  let show = $state(false);
  let testing = $state(false);

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
</script>

<PageHeader title="Settings" description="This browser's connection to the daemon. Everything else is configured on the daemon side." />

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
