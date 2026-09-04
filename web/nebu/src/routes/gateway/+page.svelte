<script lang="ts">
  import { api, baseUrl } from '$lib/api';
  import { listenerUrl, policyText } from '$lib/gateway';
  import { live, slotName, clock } from '$lib/state.svelte';
  import { count, enumLabel, ago } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { RouteState, type GatewayStatus } from '$proto/gateway_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { Waypoints, Plus, Trash2, KeyRound, Link } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Panel from '$lib/components/ui/Panel.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import StateBadge from '$lib/components/ui/StateBadge.svelte';
  import Badge from '$lib/components/ui/Badge.svelte';
  import Copy from '$lib/components/ui/Copy.svelte';
  import Stat from '$lib/components/ui/Stat.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import InstanceDrawer from '$lib/components/InstanceDrawer.svelte';

  let status = $state<GatewayStatus | null>(null);
  let aliasName = $state('');
  let aliasInstance = $state('');
  let adding = $state(false);
  let instanceId = $state('');

  const routes = $derived([...live.routes.values()].sort((a, b) => a.name.localeCompare(b.name)));
  const ready = $derived([...live.instances.values()].filter((i) => i.state === InstanceState.READY));
  const readyRoutes = $derived(routes.filter((r) => r.state === RouteState.READY));
  const endpoints = $derived.by(() => {
    const list = status?.listeners ?? [];
    if (list.length === 0) return [{ url: baseUrl + '/v1', shared: true, guess: true }];
    return list.map((l) => ({ url: (l.shared ? baseUrl : listenerUrl(l.addr, !!status?.tls)) + '/v1', shared: l.shared, guess: false }));
  });
  const example = $derived(readyRoutes[0]?.name ?? routes[0]?.name ?? 'main');

  const curl = $derived(
    `curl ${endpoints[0].url}/chat/completions \\\n  -H 'Content-Type: application/json' \\\n${status?.auth ? "  -H 'Authorization: Bearer $NEBU_API_KEY' \\\n" : ''}  -d '{"model":"${example}","messages":[{"role":"user","content":"hello"}]}'`
  );

  async function refresh() {
    try {
      status = (await api.gateway.getGatewayStatus({})).status ?? null;
    } catch (err) {
      fail(err, 'Gateway status failed');
    }
  }
  // Listeners, keys, and the default policy change with the daemon, counters and states ride the routes themselves
  $effect(() => {
    void [...live.routes.values()].map((r) => r.name + r.state).join();
    refresh();
  });

  async function addAlias() {
    adding = true;
    try {
      await api.gateway.setRoute({ name: aliasName.trim(), instanceId: aliasInstance });
      ok(`Route ${aliasName.trim()} added`);
      aliasName = '';
    } catch (err) {
      fail(err, 'Route refused');
    } finally {
      adding = false;
    }
  }

  async function remove(name: string) {
    const yes = await confirm({ title: `Remove route ${name}?`, message: 'Clients using this name get 404. The instance keeps running under its own name.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.gateway.deleteRoute({ name });
      ok(`Removed ${name}`);
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }
</script>

<PageHeader title="Gateway" description="One endpoint your router points at, answering in the OpenAI, Anthropic, and Ollama formats, and the names it answers to" />

<div class="flex flex-col gap-6">
  <section class="grid grid-cols-1 gap-4 lg:grid-cols-5">
    <Panel title="Endpoint" class="lg:col-span-3">
      <div class="flex flex-col gap-3">
        {#each endpoints as e (e.url)}
          <div class="flex items-center gap-2 rounded-lg border border-line bg-sunken px-3 py-2">
            <Link size={14} class="shrink-0 text-fg-faint" />
            <span class="flex-1 truncate font-mono text-sm text-fg">{e.url}</span>
            {#if e.shared}<Badge size="xs" label="shares the API listener" />{/if}
            <Copy text={e.url} />
          </div>
        {/each}
        <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-fg-muted">
          <span class="inline-flex items-center gap-1"><KeyRound size={12} /> {status?.auth ? 'API keys required' : 'No API keys configured'}</span>
          <span>models list at <span class="font-mono">{endpoints[0].url}/models</span></span>
        </div>
        <div class="relative">
          <pre class="overflow-x-auto rounded-lg border border-line bg-sunken p-3 pr-10 font-mono text-[11.5px] leading-5 text-fg-muted">{curl}</pre>
          <div class="absolute top-2 right-2"><Copy text={curl} label="Copy example" /></div>
        </div>
      </div>
    </Panel>
    <div class="grid grid-cols-2 gap-3 lg:col-span-2 lg:grid-cols-1">
      <div class="panel p-4"><Stat label="Requests served" value={count(status?.requests ?? 0n)} sub="since the daemon started" /></div>
      <div class="panel p-4"><Stat label="Routes ready" value="{readyRoutes.length} / {routes.length}" sub="{routes.filter((r) => r.slotId).length} owned by slots" /></div>
      <div class="panel p-4"><Stat label="Default limits" value={policyText(undefined, status?.policy)} sub="set in gateway.policy, a slot can tighten its own route" /></div>
    </div>
  </section>

  <Panel title="Routes" description="public names mapped to instances" flush>
    {#if routes.length === 0}
      <Empty compact icon={Waypoints} title="No routes" description="Every running instance and every slot gets a route by name. Add an alias to answer to another name too." />
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          <thead><tr><th>name</th><th>state</th><th>model</th><th>instance</th><th>slot</th><th class="num">requests</th><th class="num">in flight</th><th>limits</th><th>updated</th><th></th></tr></thead>
          <tbody>
            {#each routes as r (r.name)}
              {@const inst = r.instanceId ? live.instances.get(r.instanceId) : undefined}
              <tr>
                <td class="font-mono text-sm text-fg">{r.name}</td>
                <td><StateBadge values={RouteState} value={r.state} size="xs" /></td>
                <td class="font-mono text-xs text-fg-muted">{r.model || '–'}</td>
                <td class="text-xs">
                  {#if inst}
                    <button class="link" onclick={() => (instanceId = inst.id)}>{inst.name}</button>
                    <span class="font-mono text-fg-faint"> {r.endpoint}</span>
                  {:else if r.instanceId}
                    <span class="font-mono text-fg-faint">{r.instanceId.slice(0, 8)}</span>
                  {:else}
                    <span class="text-fg-faint">{r.state === RouteState.PENDING ? 'waiting for an instance' : '–'}</span>
                  {/if}
                </td>
                <td class="text-xs">{r.slotId ? slotName(r.slotId) : '–'}</td>
                <td class="num text-xs">{count(r.requests)}</td>
                <td class="num text-xs">{r.inFlight || '–'}</td>
                <td class="text-xs text-fg-muted" title="What this route enforces, the gateway default where it inherits">{policyText(r.policy, status?.policy)}</td>
                <td class="text-xs text-fg-muted">{ago(r.updatedAt, clock.now)}</td>
                <td class="text-right">
                  {#if !r.slotId}
                    <Button size="xs" variant="ghost" icon={Trash2} class="text-bad hover:text-bad" onclick={() => remove(r.name)}>Remove</Button>
                  {:else}
                    <span class="text-[11px] text-fg-faint">{enumLabel(RouteState, r.state) === 'pending' ? 'slot keeps it' : 'slot owned'}</span>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Panel>

  <Panel title="Add an alias" description="Answer to another model name with an instance that is already ready">
    <form
      class="flex flex-wrap items-end gap-3"
      onsubmit={(e) => {
        e.preventDefault();
        addAlias();
      }}
    >
      <Field label="Alias" for="alias-name" class="w-64">
        <input id="alias-name" class="input font-mono" bind:value={aliasName} placeholder="gpt-4o" />
      </Field>
      <Field label="Instance" for="alias-instance" class="w-80">
        <select id="alias-instance" class="input" bind:value={aliasInstance}>
          <option value="">Pick a ready instance</option>
          {#each ready as i (i.id)}<option value={i.id}>{i.name} · {i.repo.split('/').pop()}:{i.group}</option>{/each}
        </select>
      </Field>
      <Button type="submit" icon={Plus} loading={adding} disabled={!aliasName.trim() || !aliasInstance}>Add route</Button>
      {#if ready.length === 0}<span class="pb-2 text-xs text-fg-faint">Nothing is ready to alias yet.</span>{/if}
    </form>
  </Panel>
</div>

<InstanceDrawer bind:id={instanceId} />
