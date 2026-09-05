<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { page } from '$app/state';
  import { Popover } from 'bits-ui';
  import { baseUrl, gatewayKey, setGatewayKey } from '$lib/api';
  import { listenerUrl } from '$lib/gateway';
  import { live, cached } from '$lib/state.svelte';
  import { byName, enumLabel } from '$lib/format';
  import { fail } from '$lib/toast.svelte';
  import { RouteState } from '$proto/gateway_pb';
  import { MessageSquare, Send, Square, Trash2, KeyRound, SlidersHorizontal, ArrowUp } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Markdown from '$lib/components/ui/Markdown.svelte';
  import Copy from '$lib/components/ui/Copy.svelte';

  // What one streamed line carries: a delta, the final usage, or the error that ended the answer
  interface Chunk {
    error?: { message?: string };
    message?: string;
    choices?: { delta?: { content?: string }; text?: string }[];
    usage?: { completion_tokens?: number };
  }

  interface Turn {
    role: 'user' | 'assistant';
    text: string;
    tokens?: number;
    seconds?: number;
    error?: string;
  }

  let model = $state('');
  let system = $state('');
  let temperature = $state('');
  let maxTokens = $state('');
  let draft = $state('');
  let turns = $state<Turn[]>([]);
  let busy = $state(false);
  let key = $state(gatewayKey());
  let log: HTMLDivElement | undefined = $state();
  let box: HTMLTextAreaElement | undefined = $state();
  let controller: AbortController | null = null;

  const loading = $derived(!live.ready && !live.error);
  const status = $derived(cached.gateway);
  const ready = $derived([...live.routes.values()].filter((r) => r.state === RouteState.READY).sort(byName((r) => r.name)));
  // The route asked for stays chosen while it exists, even before it answers, else the first that does
  const asked = $derived(model ? live.routes.get(model) : undefined);
  const chosen = $derived(asked ?? ready[0]);
  const chosenReady = $derived(chosen?.state === RouteState.READY);
  $effect(() => {
    if (!asked && chosen && model !== chosen.name) model = chosen.name;
  });
  // The gateway on the API listener is same origin, one of its own is reached by address
  const own = $derived(status?.listeners.find((l) => !l.shared));
  const gatewayBase = $derived(own ? listenerUrl(own.addr, !!status?.tls) : baseUrl);
  const endpoint = $derived(gatewayBase + '/v1/chat/completions');
  const tuned = $derived([system.trim(), temperature.trim(), maxTokens.trim()].filter(Boolean).length);

  onMount(() => {
    model = page.url.searchParams.get('model') ?? '';
  });

  async function scroll() {
    await tick();
    log?.scrollTo({ top: log.scrollHeight });
  }

  function stop() {
    controller?.abort();
  }

  function clear() {
    stop();
    turns = [];
  }

  // The history the model sees: answered turns only, a failed or empty answer and its question left out
  function history(): { role: string; content: string }[] {
    const out: { role: string; content: string }[] = [];
    const past = turns.slice(0, -1);
    for (let i = 0; i < past.length; i++) {
      const t = past[i];
      if (t.role === 'user') {
        const answer = past[i + 1];
        if (answer?.role === 'assistant' && answer.text && !answer.error) out.push({ role: 'user', content: t.text }, { role: 'assistant', content: answer.text });
        else if (i === past.length - 1) out.push({ role: 'user', content: t.text });
      }
    }
    return out;
  }

  // Says what went wrong reaching the gateway, a blocked origin or an untrusted certificate being the usual causes
  function explain(err: unknown): string {
    const text = err instanceof Error ? err.message : String(err);
    if (err instanceof TypeError && own) {
      return `${text}. Could not reach ${gatewayBase}. Open ${gatewayBase}/health once to accept its certificate or add ${window.location.origin} to gateway.cors_origins`;
    }
    return text;
  }

  // Sends the conversation and streams the answer into the last turn
  async function send() {
    const text = draft.trim();
    if (!text || busy || !chosen || !chosenReady) return;
    draft = '';
    turns = [...turns, { role: 'user', text }, { role: 'assistant', text: '' }];
    busy = true;
    controller = new AbortController();
    const started = performance.now();
    let tokens = 0;
    scroll();
    try {
      const messages = [...(system.trim() ? [{ role: 'system', content: system.trim() }] : []), ...history()];
      const body: Record<string, unknown> = { model: chosen.name, messages, stream: true, stream_options: { include_usage: true } };
      if (temperature.trim()) body.temperature = parseFloat(temperature);
      if (maxTokens.trim()) body.max_tokens = parseInt(maxTokens, 10);
      const headers: Record<string, string> = { 'Content-Type': 'application/json' };
      if (key) headers.Authorization = 'Bearer ' + key;
      const resp = await fetch(endpoint, { method: 'POST', headers, body: JSON.stringify(body), signal: controller.signal });
      if (!resp.ok || !resp.body) {
        const raw = await resp.text();
        let detail = raw;
        try {
          detail = JSON.parse(raw).error?.message ?? raw;
        } catch {
          // not json
        }
        throw new Error(`${resp.status}: ${detail}`);
      }
      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let pending = '';
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        pending += decoder.decode(value, { stream: true });
        const lines = pending.split('\n');
        pending = lines.pop() ?? '';
        for (const line of lines) {
          // A runtime that dies mid answer says so on an error field or an error line, never on a chunk
          const field = line.startsWith('data:') ? 'data' : line.startsWith('error:') ? 'error' : '';
          if (!field) continue;
          const data = line.slice(field.length + 1).trim();
          if (data === '[DONE]') continue;
          let chunk: Chunk;
          try {
            chunk = JSON.parse(data);
          } catch {
            if (field === 'error') throw new Error(data);
            continue;
          }
          if (field === 'error' || chunk.error) throw new Error(chunk.error?.message ?? chunk.message ?? data);
          const delta = chunk.choices?.[0]?.delta?.content ?? chunk.choices?.[0]?.text ?? '';
          if (delta) {
            turns[turns.length - 1].text += delta;
            tokens++;
            scroll();
          }
          if (chunk.usage?.completion_tokens) tokens = chunk.usage.completion_tokens;
        }
      }
    } catch (err) {
      if (!(err instanceof DOMException && err.name === 'AbortError')) {
        turns[turns.length - 1].error = explain(err);
        fail(err, 'Chat failed');
      }
    } finally {
      const last = turns[turns.length - 1];
      last.tokens = tokens;
      last.seconds = (performance.now() - started) / 1000;
      busy = false;
      controller = null;
      scroll();
      box?.focus();
    }
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      send();
    }
  }
</script>

<PageHeader title="Chat" subtitle="Through the gateway itself, so what answers here is what clients get">
  {#if !loading && (ready.length || asked)}
    {#if ready.length <= 1 && (!asked || chosenReady)}
      <span class="input-static w-auto font-mono">{chosen?.name ?? '–'}</span>
    {:else}
      <select class="input w-auto font-mono" bind:value={model} aria-label="Model">
        {#if asked && !chosenReady}<option value={asked.name}>{asked.name} · {enumLabel(RouteState, asked.state)}</option>{/if}
        {#each ready as r (r.name)}<option value={r.name}>{r.name}{r.served && r.served !== r.name ? ` · ${r.served}` : ''}</option>{/each}
      </select>
    {/if}
    <Popover.Root>
      <Popover.Trigger class="inline-flex h-9 items-center gap-2 rounded-lg border border-line bg-raised px-3.5 text-sm font-medium text-fg transition-colors hover:border-line-strong data-[state=open]:border-accent/60">
        <SlidersHorizontal size={16} /> Options{#if tuned}<span class="rounded-full bg-accent/15 px-1.5 text-xs text-accent">{tuned}</span>{/if}
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={6} class="enter-up z-[60] flex w-80 flex-col gap-4 rounded-xl border border-line bg-overlay p-4 shadow-pop focus:outline-none">
          <Field label="System prompt" for="chat-system">
            <textarea id="chat-system" class="input h-24" bind:value={system} placeholder="Optional"></textarea>
          </Field>
          <div class="grid grid-cols-2 gap-3">
            <Field label="Temperature" for="chat-temp">
              <input id="chat-temp" class="input font-mono" inputmode="decimal" bind:value={temperature} placeholder="default" />
            </Field>
            <Field label="Max tokens" for="chat-max">
              <input id="chat-max" class="input font-mono" inputmode="numeric" bind:value={maxTokens} placeholder="default" />
            </Field>
          </div>
          {#if status?.auth}
            <Field label="Gateway key" for="chat-key" hint="Kept in this browser">
              <div class="relative">
                <KeyRound size={14} class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-fg-faint" />
                <input id="chat-key" class="input pl-9 font-mono" type="password" bind:value={key} onchange={() => setGatewayKey(key.trim())} autocomplete="off" />
              </div>
            </Field>
          {/if}
          <p class="truncate font-mono text-xs text-fg-faint" title={endpoint}>{endpoint}</p>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
    <Button variant="ghost" icon={Trash2} onclick={clear} disabled={!turns.length}>Clear</Button>
  {/if}
</PageHeader>

{#if loading}
  <section class="card h-[calc(100vh-12rem)] min-h-[28rem]" aria-busy="true"></section>
{:else if ready.length === 0 && !asked}
  <div class="card">
    <Empty icon={MessageSquare} title="Nothing is serving yet">
      {#if live.models.size}<Button variant="primary" href="/store">Open the library</Button>{:else}<Button variant="primary" href="/catalog">Discover models</Button>{/if}
    </Empty>
  </div>
{:else}
  <section class="card flex h-[calc(100vh-12rem)] min-h-[28rem] flex-col">
    {#if chosen && !chosenReady}
      <div class="note note-warn m-4 mb-0">{chosen.name} is {enumLabel(RouteState, chosen.state)}</div>
    {/if}
    <div bind:this={log} class="flex-1 overflow-y-auto px-5 py-5">
      <div class="mx-auto flex max-w-3xl flex-col gap-5">
        {#each turns as t, i (i)}
          <div class="flex flex-col gap-1 {t.role === 'user' ? 'items-end' : 'items-start'}">
            <div class="max-w-[88%] rounded-xl px-4 py-2.5 text-sm {t.role === 'user' ? 'bg-accent/12 text-fg' : 'border border-line bg-sunken text-fg'}">
              {#if t.role === 'user'}
                <span class="whitespace-pre-wrap">{t.text}</span>
              {:else if t.text}
                <Markdown markdown={t.text} />
              {:else if !t.error}
                <span class="text-fg-faint">…</span>
              {/if}
              {#if t.error}
                <div class="text-sm text-bad {t.text ? 'mt-2 border-t border-bad/30 pt-2' : ''}">{t.error}</div>
              {/if}
            </div>
            {#if t.role === 'assistant' && t.seconds !== undefined}
              <span class="inline-flex items-center gap-1.5 text-xs tabular-nums text-fg-faint">
                {t.tokens ?? 0} tokens · {t.seconds.toFixed(1)}s{t.seconds && t.tokens ? ` · ${(t.tokens / t.seconds).toFixed(1)} tok/s` : ''}
                {#if t.text}<Copy text={t.text} size={13} class="h-6 w-6" />{/if}
              </span>
            {/if}
          </div>
        {/each}
      </div>
    </div>
    <div class="border-t border-line p-4">
      {#if turns.length === 0}
        <p class="mx-auto mb-3 max-w-3xl text-sm text-fg-muted">Say something to <span class="font-mono text-fg">{chosen?.name}</span></p>
      {/if}
      <div class="mx-auto flex max-w-3xl items-end gap-2 rounded-xl border border-line bg-sunken p-2 focus-within:border-accent">
        <textarea bind:this={box} class="max-h-40 min-h-10 flex-1 resize-none bg-transparent px-2 py-2 text-sm text-fg placeholder:text-fg-faint focus:outline-none" rows="1" bind:value={draft} onkeydown={onKey} placeholder="Message {chosen?.name ?? ''}"></textarea>
        {#if busy}
          <Button variant="danger" icon={Square} onclick={stop} aria-label="Stop" />
        {:else}
          <Button variant="primary" icon={ArrowUp} onclick={send} disabled={!draft.trim() || !chosenReady} aria-label="Send" />
        {/if}
      </div>
      <p class="mx-auto mt-1.5 max-w-3xl text-xs text-fg-faint">Enter sends, Shift+Enter breaks a line</p>
    </div>
  </section>
{/if}
