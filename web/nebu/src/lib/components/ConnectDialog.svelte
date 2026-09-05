<script lang="ts">
  import { api, baseUrl } from '$lib/api';
  import { listenerUrl, policyText } from '$lib/gateway';
  import { live, cached, refreshCached, slotName } from '$lib/state.svelte';
  import { byName, count } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { RouteState } from '$proto/gateway_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { Plus, Trash2, KeyRound, LockOpen } from '@lucide/svelte';
  import Dialog from './ui/Dialog.svelte';
  import Button from './ui/Button.svelte';
  import Copy from './ui/Copy.svelte';
  import Segmented from './ui/Segmented.svelte';
  import Section from './ui/Section.svelte';
  import StatePill from './ui/StatePill.svelte';
  import Empty from './ui/Empty.svelte';

  type Flavor = 'openai' | 'anthropic' | 'ollama';

  // How clients reach the gateway: its addresses, a request in each dialect, and every name it answers to
  let { open = $bindable(false) }: { open?: boolean } = $props();

  let flavor = $state<Flavor>('openai');
  let aliasName = $state('');
  let aliasInstance = $state('');
  let adding = $state(false);

  const status = $derived(cached.gateway);
  const routes = $derived([...live.routes.values()].sort(byName((r) => r.name)));
  const ready = $derived([...live.instances.values()].filter((i) => i.state === InstanceState.READY));
  const readyRoutes = $derived(routes.filter((r) => r.state === RouteState.READY));
  // One origin per listener, the shared one being the address this page came from
  const origins = $derived.by(() => {
    const list = status?.listeners ?? [];
    if (list.length === 0) return [{ url: baseUrl, shared: true }];
    return list.map((l) => ({ url: l.shared ? baseUrl : listenerUrl(l.addr, !!status?.tls), shared: l.shared }));
  });
  const base = $derived(origins[0].url);
  const auth = $derived(!!status?.auth);
  const example = $derived(readyRoutes[0]?.name ?? routes[0]?.name ?? 'main');
  const nameTaken = $derived(live.routes.has(aliasName.trim()));

  const examples = $derived<Record<Flavor, string>>({
    openai: `curl ${base}/v1/chat/completions \\\n  -H 'Content-Type: application/json' \\\n${auth ? "  -H 'Authorization: Bearer $NEBU_API_KEY' \\\n" : ''}  -d '{"model":"${example}","messages":[{"role":"user","content":"hello"}]}'`,
    anthropic: `curl ${base}/v1/messages \\\n  -H 'Content-Type: application/json' \\\n  -H 'anthropic-version: 2023-06-01' \\\n${auth ? "  -H 'x-api-key: $NEBU_API_KEY' \\\n" : ''}  -d '{"model":"${example}","max_tokens":256,"messages":[{"role":"user","content":"hello"}]}'`,
    ollama: `curl ${base}/api/chat \\\n  -H 'Content-Type: application/json' \\\n${auth ? "  -H 'Authorization: Bearer $NEBU_API_KEY' \\\n" : ''}  -d '{"model":"${example}","messages":[{"role":"user","content":"hello"}],"stream":false}'`
  });

  // The request total only moves with traffic, so each opening reads it fresh
  $effect(() => {
    if (open) void refreshCached();
  });

  async function addAlias() {
    adding = true;
    try {
      await api.gateway.setRoute({ name: aliasName.trim(), instanceId: aliasInstance });
      ok(`${aliasName.trim()} now answers`);
      aliasName = '';
    } catch (err) {
      fail(err, 'Alias refused');
    } finally {
      adding = false;
    }
  }

  async function remove(name: string) {
    const yes = await confirm({ title: `Remove ${name}?`, message: 'Clients using this name get 404.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.gateway.deleteRoute({ name });
      ok(`Removed ${name}`);
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }
</script>

<Dialog bind:open title="Connect" description="OpenAI, Anthropic, and Ollama clients all work against this host" size="lg">
  <div class="flex flex-col gap-6">
    <Section title="Endpoint">
      <div class="overflow-hidden rounded-lg border border-line">
        {#each origins as o (o.url)}
          <div class="flex items-center gap-3 border-b border-line/60 px-3 py-2 last:border-b-0">
            <span class="min-w-0 flex-1 truncate font-mono text-sm text-fg">{o.url}</span>
            {#if o.shared}<span class="text-xs text-fg-faint">API listener</span>{/if}
            <Copy text={o.url} />
          </div>
        {/each}
        <div class="flex flex-wrap items-center gap-x-5 gap-y-1 border-t border-line bg-sunken/60 px-3 py-2 text-sm text-fg-muted">
          <span class="inline-flex items-center gap-1.5">
            {#if auth}<KeyRound size={14} class="text-warn" /> Needs an API key{:else}<LockOpen size={14} /> No key needed{/if}
          </span>
          {#if status?.tls}<span>TLS</span>{/if}
          <span>Limits {policyText(undefined, status?.policy)}</span>
          <span class="ml-auto tabular-nums">{count(status?.requests ?? 0n)} requests since start</span>
        </div>
      </div>
    </Section>

    <Section title="Example request">
      {#snippet actions()}
        <Segmented size="sm" bind:value={flavor} tabs={[{ id: 'openai', label: 'OpenAI' }, { id: 'anthropic', label: 'Anthropic' }, { id: 'ollama', label: 'Ollama' }]} />
      {/snippet}
      <div class="relative">
        <pre class="code pr-12">{examples[flavor]}</pre>
        <div class="absolute top-2 right-2"><Copy text={examples[flavor]} /></div>
      </div>
    </Section>

    <Section title="Model names" description="What clients send as the model">
      {#if routes.length === 0}
        <div class="rounded-lg border border-line"><Empty compact title="Nothing answers yet" /></div>
      {:else}
        <div class="overflow-x-auto rounded-lg border border-line">
          <table class="tbl">
            <thead><tr><th>Name</th><th>State</th><th>Serves</th><th class="num">Requests</th><th>Limits</th><th></th></tr></thead>
            <tbody>
              {#each routes as r (r.name)}
                {@const inst = r.instanceId ? live.instances.get(r.instanceId) : undefined}
                <tr>
                  <td class="font-mono text-fg">{r.name}</td>
                  <td><StatePill values={RouteState} value={r.state} /></td>
                  <td class="text-fg-muted">
                    {#if r.slotId}slot {slotName(r.slotId)}{:else if inst}{inst.name}{:else if r.state === RouteState.PENDING}waiting{:else}–{/if}
                    {#if r.model && r.model !== r.name}<span class="font-mono text-fg-faint">{' · '}{r.model}</span>{/if}
                  </td>
                  <td class="num">{count(r.requests)}{#if r.inFlight}<span class="text-fg-faint">{' · '}{r.inFlight} live</span>{/if}</td>
                  <td class="text-fg-muted">{policyText(r.policy, status?.policy)}</td>
                  <td class="actions">{#if !r.slotId}<Button size="sm" variant="ghost" icon={Trash2} aria-label="Remove {r.name}" onclick={() => remove(r.name)} />{/if}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
      {#if ready.length}
        <form
          class="mt-3 flex flex-wrap items-center gap-2"
          onsubmit={(e) => {
            e.preventDefault();
            if (aliasName.trim() && !nameTaken && aliasInstance) addAlias();
          }}
        >
          <input class="input h-8 w-44 font-mono" bind:value={aliasName} placeholder="Another name" aria-label="Alias name" aria-invalid={nameTaken} autocomplete="off" spellcheck="false" />
          <span class="text-sm text-fg-faint">for</span>
          <select class="input h-8 w-auto" bind:value={aliasInstance} aria-label="Instance">
            <option value="">Pick an instance</option>
            {#each ready as i (i.id)}<option value={i.id}>{i.name} · {i.repo.split('/').pop()}:{i.group}</option>{/each}
          </select>
          <Button type="submit" size="sm" icon={Plus} loading={adding} disabled={!aliasName.trim() || nameTaken || !aliasInstance}>Add alias</Button>
          {#if nameTaken}<span class="text-xs text-bad">That name is taken</span>{/if}
        </form>
      {/if}
    </Section>
  </div>
</Dialog>
