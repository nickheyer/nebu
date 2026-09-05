<script lang="ts">
  import { api, baseUrl } from '$lib/api';
  import { listenerUrl, policyText } from '$lib/gateway';
  import { live, cached, refreshCached, slotName } from '$lib/state.svelte';
  import { byName, count, tail } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { RouteState } from '$proto/gateway_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { Plus, Trash2, KeyRound, LockOpen, ShieldCheck } from '@lucide/svelte';
  import Dialog from './ui/Dialog.svelte';
  import Button from './ui/Button.svelte';
  import IconButton from './ui/IconButton.svelte';
  import Copy from './ui/Copy.svelte';
  import Segmented from './ui/Segmented.svelte';
  import Section from './ui/Section.svelte';
  import Select from './ui/Select.svelte';
  import State from './ui/State.svelte';
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

<Dialog bind:open title="Connect" size="lg">
  <div class="flex flex-col gap-7">
    <Section title="Endpoint">
      {#snippet actions()}
        <span class="inline-flex items-center gap-1.5 text-xs text-fg-muted">
          {#if auth}<KeyRound size={13} class="text-warn" /> API key{:else}<LockOpen size={13} /> No key{/if}
        </span>
        {#if status?.tls}<span class="inline-flex items-center gap-1.5 text-xs text-fg-muted"><ShieldCheck size={13} /> TLS</span>{/if}
      {/snippet}
      <div class="divide-y divide-line rounded-md border border-line">
        {#each origins as o (o.url)}
          <div class="flex h-10 items-center gap-3 px-3">
            <span class="min-w-0 flex-1 truncate font-mono text-sm text-fg">{o.url}</span>
            {#if o.shared}<span class="text-xs text-fg-faint">API listener</span>{/if}
            <Copy text={o.url} />
          </div>
        {/each}
      </div>
      <div class="flex flex-wrap gap-x-5 gap-y-1 text-xs text-fg-faint">
        <span>limits {policyText(undefined, status?.policy)}</span>
        <span class="tabular-nums">{count(status?.requests ?? 0n)} requests since start</span>
      </div>
    </Section>

    <Section title="Request">
      {#snippet actions()}
        <Segmented size="sm" bind:value={flavor} tabs={[{ id: 'openai', label: 'OpenAI' }, { id: 'anthropic', label: 'Anthropic' }, { id: 'ollama', label: 'Ollama' }]} />
      {/snippet}
      <div class="relative">
        <pre class="code pr-12">{examples[flavor]}</pre>
        <div class="absolute top-1.5 right-1.5"><Copy text={examples[flavor]} /></div>
      </div>
    </Section>

    <Section title="Model names" count={routes.length} info="What clients send as the model">
      {#if routes.length === 0}
        <Empty compact title="Nothing answers yet" />
      {:else}
        <table class="tbl">
          <thead><tr><th>Name</th><th>State</th><th>Serves</th><th class="num">Requests</th><th>Limits</th><th></th></tr></thead>
          <tbody>
            {#each routes as r (r.name)}
              {@const inst = r.instanceId ? live.instances.get(r.instanceId) : undefined}
              <tr>
                <td class="font-mono text-xs text-fg">{r.name}</td>
                <td><State values={RouteState} value={r.state} /></td>
                <td class="text-fg-muted">
                  {#if r.slotId}slot {slotName(r.slotId)}{:else if inst}{inst.name}{:else if r.state === RouteState.PENDING}waiting{:else}–{/if}
                  {#if r.model && r.model !== r.name}<span class="font-mono text-xs text-fg-faint">{' · '}{r.model}</span>{/if}
                </td>
                <td class="num">{count(r.requests)}{#if r.inFlight}<span class="text-fg-faint">{' · '}{r.inFlight} live</span>{/if}</td>
                <td class="text-xs text-fg-muted">{policyText(r.policy, status?.policy)}</td>
                <td class="actions"><span>{#if !r.slotId}<IconButton size="sm" icon={Trash2} label="Remove" onclick={() => remove(r.name)} />{/if}</span></td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
      {#if ready.length}
        <form
          class="flex flex-wrap items-center gap-2"
          onsubmit={(e) => {
            e.preventDefault();
            if (aliasName.trim() && !nameTaken && aliasInstance) addAlias();
          }}
        >
          <input class="input w-44 font-mono" bind:value={aliasName} placeholder="alias" aria-label="Alias" aria-invalid={nameTaken} autocomplete="off" spellcheck="false" />
          <span class="text-sm text-fg-faint">for</span>
          <Select class="w-64" bind:value={aliasInstance} label="Instance" placeholder="Instance" items={ready.map((i) => ({ value: i.id, label: i.name, detail: `${tail(i.repo)}:${i.group}` }))} />
          <Button type="submit" size="md" icon={Plus} loading={adding} disabled={!aliasName.trim() || nameTaken || !aliasInstance}>Add</Button>
          {#if nameTaken}<span class="text-xs text-bad">That name is taken</span>{/if}
        </form>
      {/if}
    </Section>
  </div>
</Dialog>
