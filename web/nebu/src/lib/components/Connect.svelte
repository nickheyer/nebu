<script lang="ts">
  import { baseUrl } from '$lib/api';
  import { live, cached, refreshCached } from '$lib/state.svelte';
  import { dialects, listenerUrl, policyText } from '$lib/gateway';
  import { byName, count } from '$lib/format';
  import { RouteState } from '$proto/gateway_pb';
  import { KeyRound, LockOpen, ShieldCheck } from '@lucide/svelte';
  import Section from './ui/Section.svelte';
  import Segmented from './ui/Segmented.svelte';
  import Copy from './ui/Copy.svelte';

  // How clients reach the gateway: each listener's base URL per wire format, and a first request
  let dialect = $state<string>('openai');

  const status = $derived(cached.gateway);
  // One origin per listener, the shared one being the address this page came from
  const origins = $derived.by(() => {
    const list = status?.listeners ?? [];
    if (list.length === 0) return [{ url: baseUrl, shared: true }];
    return list.map((l) => ({ url: l.shared ? baseUrl : listenerUrl(l.addr, !!status?.tls), shared: l.shared }));
  });
  const auth = $derived(!!status?.auth);
  const ready = $derived([...live.routes.values()].filter((r) => r.state === RouteState.READY).sort(byName((r) => r.name)));
  const example = $derived(ready[0]?.name ?? [...live.routes.values()].sort(byName((r) => r.name))[0]?.name ?? 'main');
  const chosen = $derived(dialects.find((d) => d.id === dialect) ?? dialects[0]);
  const curl = $derived(chosen.curl(origins[0].url, example, auth));

  // The request total only moves with traffic, so the card reads it once when shown
  $effect(() => {
    void refreshCached();
  });
</script>

<Section title="Endpoints" meta={status ? `${count(status.requests)} requests since start` : ''}>
  {#snippet actions()}
    <span class="inline-flex items-center gap-1.5 text-xs text-fg-muted">
      {#if auth}<KeyRound size={13} class="text-warn" /> API key required{:else}<LockOpen size={13} /> No API key{/if}
    </span>
    {#if status?.tls}<span class="inline-flex items-center gap-1.5 text-xs text-fg-muted"><ShieldCheck size={13} /> TLS</span>{/if}
  {/snippet}
  <div class="grid grid-cols-1 gap-6 xl:grid-cols-2">
    <div class="flex flex-col gap-4">
      {#each origins as o (o.url)}
        <table class="tbl">
          <thead>
            <tr>
              <th>API</th>
              <th>Base URL{#if origins.length > 1}<span class="ml-2 font-normal normal-case tracking-normal text-fg-faint">{o.shared ? 'API listener' : 'gateway listener'}</span>{/if}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {#each dialects as d (d.id)}
              <tr>
                <td class="text-fg">{d.label}</td>
                <td class="font-mono text-xs text-fg">{o.url}{d.base}</td>
                <td class="actions"><span><Copy text={o.url + d.base} size={13} /></span></td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/each}
      <div class="flex flex-wrap gap-x-5 gap-y-1 text-xs text-fg-faint">
        <span class="kv"><span>default limits</span><span>{policyText(undefined, status?.policy)}</span></span>
        {#if auth}<span class="kv"><span>key header</span><span>{chosen.header}</span></span>{/if}
      </div>
    </div>
    <div class="flex flex-col gap-3">
      <Segmented size="sm" bind:value={dialect} tabs={dialects.map((d) => ({ id: d.id, label: d.label }))} />
      <div class="relative">
        <pre class="code pr-12">{curl}</pre>
        <div class="absolute top-1.5 right-1.5"><Copy text={curl} /></div>
      </div>
    </div>
  </div>
</Section>
