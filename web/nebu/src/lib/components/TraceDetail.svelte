<script lang="ts">
  import { api, message } from '$lib/api';
  import { live, slotName } from '$lib/state.svelte';
  import { enumLabel, millisBetween, ms, rate, when, bytes } from '$lib/format';
  import { TraceKind, type Trace } from '$proto/gateway_pb';
  import { ApiFlavor } from '$proto/runtime_pb';
  import Tabs from './ui/Tabs.svelte';
  import Stat from './ui/Stat.svelte';
  import Json from './ui/Json.svelte';
  import Skeleton from './ui/Skeleton.svelte';

  // One request in full: its timing, tokens, and the bodies that crossed the gateway
  let { id }: { id: string } = $props();

  let full = $state<Trace | null>(null);
  let error = $state('');
  let tab = $state('request');

  // The stream carries the figures as they settle; the bodies are read when the trace opens and again once it ends
  const summary = $derived(live.traces.get(id));
  const trace = $derived(full && full.id === id ? { ...summary, ...full, request: full.request, upstreamRequest: full.upstreamRequest, response: full.response } : summary);
  const finished = $derived(!!summary?.finishedAt);

  $effect(() => {
    const current = id;
    void finished;
    error = '';
    if (!current) return;
    api.gateway
      .getTrace({ id: current })
      .then((r) => {
        if (r.trace && r.trace.id === current) full = r.trace;
      })
      .catch((err) => (error = message(err)));
  });
  $effect(() => {
    void id;
    full = null;
  });

  const flavor = (f: ApiFlavor) => enumLabel(ApiFlavor, f).replace('unspecified', 'openai');
  const firstByte = $derived(millisBetween(trace?.startedAt, trace?.firstByteAt));
  const firstToken = $derived(millisBetween(trace?.startedAt, trace?.firstTokenAt));
  const total = $derived(millisBetween(trace?.startedAt, trace?.finishedAt));
  const generating = $derived(millisBetween(trace?.firstTokenAt, trace?.finishedAt));
  const tabs = $derived([
    { id: 'request', label: 'Request' },
    ...(trace?.translated ? [{ id: 'upstream', label: 'Sent to runtime' }] : []),
    { id: 'response', label: 'Response' },
    ...(trace?.toolCalls.length ? [{ id: 'tools', label: 'Tool calls', count: trace.toolCalls.length }] : [])
  ]);
  const shownTab = $derived(tabs.some((t) => t.id === tab) ? tab : 'request');
</script>

{#if !trace}
  <p class="text-sm text-fg-faint">This request is no longer kept.</p>
{:else}
  <div class="flex flex-col gap-5">
    <div class="grid grid-cols-3 gap-x-5 gap-y-4">
      <Stat label="Status" value={finished ? String(trace.status) : 'In flight'} tone={!finished ? 'default' : trace.status >= 500 ? 'bad' : trace.status >= 400 ? 'warn' : 'ok'} />
      <Stat label="Kind" value={enumLabel(TraceKind, trace.kind)} sub={trace.stream ? 'streamed' : ''} />
      <Stat label="Format" value={trace.translated ? `${flavor(trace.clientApi)} → ${flavor(trace.upstreamApi)}` : flavor(trace.clientApi)} mono />
      <Stat label="First byte" value={ms(firstByte)} />
      <Stat label="First token" value={ms(firstToken)} />
      <Stat label="Total" value={ms(total)} />
      <Stat label="Prompt" value={trace.promptTokens || '–'} sub={trace.promptTokens ? 'tokens' : ''} />
      <Stat label="Output" value={trace.completionTokens || '–'} sub={trace.completionTokens ? 'tokens' : ''} />
      <Stat label="Speed" value={rate(trace.completionTokens, generating)} />
      <Stat label="Model" value={trace.route || '–'} mono />
      <Stat label="Stop" value={trace.stop || '–'} />
      <Stat label="Bytes" value={`${bytes(trace.requestBytes, 0)} in, ${bytes(trace.responseBytes, 0)} out`} />
    </div>
    {#if trace.error}<div class="note note-bad">{trace.error}</div>{/if}
    <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-fg-faint">
      <span class="kv"><span>id</span><span>{trace.id}</span></span>
      <span class="kv"><span>path</span><span>{trace.path}</span></span>
      {#if trace.remote}<span class="kv"><span>from</span><span>{trace.remote}</span></span>{/if}
      {#if trace.slotId}<span class="kv"><span>slot</span><a class="link font-mono" href="/slots/{trace.slotId}">{slotName(trace.slotId)}</a></span>{/if}
      {#if trace.instanceId}<span class="kv"><span>instance</span><a class="link font-mono" href="/instances/{trace.instanceId}">{trace.instanceId.slice(0, 12)}</a></span>{/if}
      <span class="kv"><span>started</span><span>{when(trace.startedAt)}</span></span>
    </div>
    <div>
      <Tabs size="sm" bind:value={() => shownTab, (v) => (tab = v)} {tabs} class="mb-3" />
      {#if error}
        <div class="note note-bad">{error}</div>
      {:else if !full}
        <Skeleton rows={4} />
      {:else if shownTab === 'request'}
        <Json text={full.request} empty="Empty body" />
      {:else if shownTab === 'upstream'}
        <Json text={full.upstreamRequest} empty="Passed through unchanged" />
      {:else if shownTab === 'response'}
        {#if !finished}
          <p class="text-sm text-fg-faint">Still answering.</p>
        {:else if trace.kind === TraceKind.CHAT || trace.kind === TraceKind.GENERATE}
          {#if full.response}
            <pre class="code max-h-96 overflow-auto whitespace-pre-wrap">{full.response}</pre>
          {:else}
            <p class="text-sm text-fg-faint">No text in the answer.</p>
          {/if}
        {:else}
          <Json text={full.response} empty="Empty body" />
        {/if}
      {:else if shownTab === 'tools'}
        <div class="flex flex-col gap-3">
          {#each full.toolCalls as c (c.id + c.name)}
            <div class="card p-3">
              <div class="mb-2 flex items-baseline gap-2"><span class="font-mono text-sm text-fg">{c.name}</span><span class="font-mono text-xs text-fg-faint">{c.id}</span></div>
              <Json text={c.arguments} height="max-h-60" empty="No arguments" />
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </div>
{/if}
