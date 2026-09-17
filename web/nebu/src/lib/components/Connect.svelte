<script lang="ts">
  import { live, cached, refreshCached } from '$lib/state.svelte';
  import { dialects, listenerUrl } from '$lib/gateway';
  import { byName, count } from '$lib/format';
  import { RouteState } from '$proto/gateway_pb';
  import { KeyRound, LockOpen, ShieldCheck } from '@lucide/svelte';
  import Section from './ui/Section.svelte';
  import Segmented from './ui/Segmented.svelte';
  import Copy from './ui/Copy.svelte';

  // How clients reach the gateway in the dialect picked
  let dialect = $state<string>('openai');

  const status = $derived(cached.gateway);
  // Every address the daemon answers on, as the daemon reports it, the shared one being the API listener itself
  const origins = $derived((status?.listeners ?? []).map((l) => ({ url: listenerUrl(l.addr, !!status?.tls), shared: l.shared })));
  const auth = $derived(!!status?.auth);
  const ready = $derived([...live.routes.values()].filter((r) => r.state === RouteState.READY).sort(byName((r) => r.name)));
  const example = $derived(ready[0]?.name ?? [...live.routes.values()].sort(byName((r) => r.name))[0]?.name ?? 'main');
  const chosen = $derived(dialects.find((d) => d.id === dialect) ?? dialects[0]);
  const origin = $derived(origins[0]?.url ?? '');
  const curl = $derived(origin ? chosen.curl(origin, example, auth) : '');

  // The request total only moves with traffic, so the card reads it once when shown
  $effect(() => {
    void refreshCached();
  });
</script>

<Section title="Endpoints" meta={status ? `${count(status.requests)} requests since start` : ''}>
  {#snippet actions()}
    <span class="inline-flex items-center gap-1.5 text-xs text-fg-muted">
      {#if auth}<KeyRound size={13} class="text-warn" /> API key in <span class="font-mono">{chosen.header}</span>{:else}<LockOpen size={13} /> No API key{/if}
    </span>
    {#if status?.tls}<span class="inline-flex items-center gap-1.5 text-xs text-fg-muted"><ShieldCheck size={13} /> TLS</span>{/if}
  {/snippet}
  {#if !status}
    <div class="skeleton h-14" aria-busy="true"></div>
  {:else}
    <div class="flex flex-col gap-3">
      <Segmented size="sm" bind:value={dialect} tabs={dialects.map((d) => ({ id: d.id, label: d.label }))} />
      <div class="overflow-x-auto">
        <table class="tbl dense">
          <tbody>
            {#each origins as o (o.url)}
              <tr>
                <td class="whitespace-nowrap text-fg-muted">Base URL{#if origins.length > 1}<span class="ml-1 text-xs text-fg-faint">{o.shared ? 'API listener' : 'gateway listener'}</span>{/if}</td>
                <td class="w-full font-mono text-xs text-fg">{o.url}{chosen.base}</td>
                <td class="actions"><span><Copy text={o.url + chosen.base} label="Copy the base URL" size={12} /></span></td>
              </tr>
            {/each}
            {#each chosen.endpoints as e (e.path)}
              <tr>
                <td class="font-mono text-xs text-fg-faint">{e.method}</td>
                <td class="w-full font-mono text-xs text-fg-muted">{origin}{e.path}</td>
                <td class="actions"><span><Copy text={origin + e.path} label="Copy {e.path}" size={12} /></span></td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <div class="relative">
        <pre class="code pr-12">{curl}</pre>
        <div class="absolute top-1.5 right-1.5"><Copy text={curl} /></div>
      </div>
    </div>
  {/if}
</Section>
