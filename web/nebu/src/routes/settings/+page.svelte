<script lang="ts">
  import { api, message, setToken, token, baseUrl } from '$lib/api';
  import { connect, live } from '$lib/state.svelte';
  import { enumName } from '$lib/format';
  import Badge from '$lib/components/Badge.svelte';
  import { RouteState } from '$proto/gateway_pb';
  import { InstanceState } from '$proto/instance_pb';
  import type { GatewayStatus } from '$proto/gateway_pb';

  let value = $state(token());
  let status = $state<GatewayStatus | null>(null);
  let error = $state('');
  let aliasName = $state('');
  let aliasInstance = $state('');

  const routes = $derived([...live.routes.values()].sort((a, b) => a.name.localeCompare(b.name)));
  const ready = $derived([...live.instances.values()].filter((i) => i.state === InstanceState.READY));

  async function refresh() {
    try {
      status = (await api.gateway.getGatewayStatus({})).status ?? null;
    } catch (err) {
      error = message(err);
    }
  }
  $effect(() => {
    refresh();
  });

  function save() {
    setToken(value.trim());
    connect();
    refresh();
  }

  async function addAlias() {
    error = '';
    try {
      await api.gateway.setRoute({ name: aliasName, instanceId: aliasInstance });
      aliasName = '';
    } catch (err) {
      error = message(err);
    }
  }

  async function removeRoute(name: string) {
    error = '';
    try {
      await api.gateway.deleteRoute({ name });
    } catch (err) {
      error = message(err);
    }
  }
</script>

<div class="space-y-4">
  <h1 class="h1">Settings</h1>
  {#if error}<div class="text-sm text-red-300">{error}</div>{/if}
  <div class="card space-y-2">
    <h2 class="h2">API token</h2>
    <p class="muted text-sm">Required when the daemon sets auth.token, stored only in this browser.</p>
    <div class="flex gap-2">
      <input class="input max-w-md" type="password" bind:value placeholder="token" />
      <button class="btn btn-primary" onclick={save}>save</button>
    </div>
    <div class="muted text-xs">api {baseUrl}, stream {live.connected ? 'connected' : 'disconnected'} {live.error}</div>
  </div>

  <div class="card space-y-2">
    <h2 class="h2">Gateway</h2>
    {#if status}
      {#each status.listeners as l (l.addr)}
        <div class="text-sm">OpenAI compatible endpoint at <span class="mono">http://{l.addr}/v1</span>{l.shared ? ', shared with the API listener' : ''}</div>
      {/each}
      <div class="muted text-sm">api keys {status.auth ? 'required' : 'not required'}, {status.requests.toString()} requests served</div>
    {/if}
    <table class="table">
      <thead><tr><th>name</th><th>state</th><th>model</th><th>instance</th><th>slot</th><th>requests</th><th>in flight</th><th></th></tr></thead>
      <tbody>
        {#each routes as r (r.name)}
          <tr>
            <td class="mono">{r.name}</td>
            <td><Badge state={enumName(RouteState, r.state)} /></td>
            <td class="mono">{r.model}</td>
            <td class="mono">{r.instanceId}</td>
            <td>{r.slotId ? live.slots.get(r.slotId)?.name ?? r.slotId : ''}</td>
            <td>{r.requests.toString()}</td>
            <td>{r.inFlight}</td>
            <td class="text-right">{#if !r.slotId}<button class="btn btn-danger" onclick={() => removeRoute(r.name)}>remove</button>{/if}</td>
          </tr>
        {:else}
          <tr><td colspan="8" class="muted">no routes</td></tr>
        {/each}
      </tbody>
    </table>
    <div class="flex gap-2">
      <input class="input max-w-xs" bind:value={aliasName} placeholder="alias name" />
      <select class="input max-w-xs" bind:value={aliasInstance}>
        <option value="">ready instance</option>
        {#each ready as i (i.id)}<option value={i.id}>{i.name} ({i.repo}:{i.group})</option>{/each}
      </select>
      <button class="btn" onclick={addAlias} disabled={!aliasName || !aliasInstance}>add alias</button>
    </div>
  </div>
</div>
