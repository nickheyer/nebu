<script lang="ts">
  import { live, cached, refreshCached } from '$lib/state.svelte';
  import { curl, dialects, keyHeader, listenerUrl } from '$lib/gateway';
  import { byName, count } from '$lib/format';
  import { RouteState } from '$proto/gateway_pb';
  import { KeyRound, LockOpen, ShieldCheck } from '@lucide/svelte';
  import Section from './ui/Section.svelte';
  import Segmented from './ui/Segmented.svelte';
  import Copy from './ui/Copy.svelte';

  let dialect = $state<string>('openai');

  const status = $derived(cached.gateway);
  const origins = $derived((status?.listeners ?? []).map((l) => ({ url: listenerUrl(l.addr, !!status?.tls), shared: l.shared })));
  const auth = $derived(!!status?.auth);
  const ready = $derived([...live.routes.values()].filter((r) => r.state === RouteState.READY).sort(byName((r) => r.name)));
  const example = $derived(ready[0]?.name ?? [...live.routes.values()].sort(byName((r) => r.name))[0]?.name ?? 'main');
  const chosen = $derived(dialects.find((d) => d.id === dialect) ?? dialects[0]);
  const origin = $derived(origins[0]?.url ?? '');
  const snippet = $derived(origin ? curl(chosen, origin, example, auth) : '');

  // Capture the request total when the card opens.
  $effect(() => {
    void refreshCached();
  });
</script>

<Section title="Endpoints" meta={status ? `${count(status.requests)} requests since start` : ''}>
  {#snippet actions()}
    <span class="inline-flex items-center gap-1.5 text-xs text-fg-muted">
      {#if auth}<KeyRound size={13} class="text-warn" /> API key in <span class="font-mono">{keyHeader(chosen)}</span>{:else}<LockOpen size={13} /> No API key{/if}
    </span>
    {#if status?.tls}<span class="inline-flex items-center gap-1.5 text-xs text-fg-muted"><ShieldCheck size={13} /> TLS</span>{/if}
  {/snippet}
  {#if !status}
    <div class="skeleton h-14" aria-busy="true"></div>
  {:else}
    <div class="flex flex-col gap-3">
      <Segmented bind:value={dialect} tabs={dialects.map((d) => ({ id: d.id, label: d.label }))} />
      <div class="tbl-wrap">
        <table class="tbl dense">
          <tbody>
            {#each origins as o (o.url)}
              <tr>
                <td class="font-mono text-xs whitespace-nowrap text-fg-faint">Base URL{#if origins.length > 1}<span class="ml-1 font-sans text-fg-faint">{o.shared ? 'API listener' : 'gateway listener'}</span>{/if}</td>
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
        <pre class="code pr-12">{snippet}</pre>
        <div class="absolute top-1.5 right-1.5"><Copy text={snippet} /></div>
      </div>
    </div>
  {/if}
</Section>
