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
  import { MessageSquare, Square, Trash2, KeyRound, SlidersHorizontal, ArrowUp } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import Select from '$lib/components/ui/Select.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Markdown from '$lib/components/ui/Markdown.svelte';
  import Copy from '$lib/components/ui/Copy.svelte';
  import Logo from '$lib/components/Logo.svelte';

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
  const modelItems = $derived([...(asked && !chosenReady ? [{ value: asked.name, label: asked.name, detail: enumLabel(RouteState, asked.state) }] : []), ...ready.map((r) => ({ value: r.name, label: r.name, detail: r.served && r.served !== r.name ? r.served : undefined }))]);

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
    box?.focus();
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
    if (box) box.style.height = '';
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

  // The box grows with the draft up to a few lines
  function grow() {
    if (!box) return;
    box.style.height = '';
    box.style.height = Math.min(box.scrollHeight, 200) + 'px';
  }
</script>

<PageHeader title="Chat">
  {#if !loading && (ready.length || asked)}
    <Select class="w-56" mono bind:value={model} label="Model" items={modelItems} />
    <Popover.Root>
      <Popover.Trigger class="inline-flex h-8 items-center gap-1.5 rounded-md border border-line bg-raised/60 px-3 text-sm font-medium text-fg transition-colors hover:border-line-strong hover:bg-raised data-[state=open]:border-accent/60">
        <SlidersHorizontal size={14} /> Options{#if tuned}<span class="rounded-full bg-accent/15 px-1.5 text-[11px] text-accent">{tuned}</span>{/if}
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="end" sideOffset={6} class="enter-up z-[60] flex w-80 flex-col gap-4 rounded-md border border-line bg-overlay p-4 shadow-pop focus:outline-none">
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
            <Field label="Gateway key" for="chat-key" info="Kept in this browser">
              <div class="relative">
                <KeyRound size={13} class="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-fg-faint" />
                <input id="chat-key" class="input pl-8 font-mono" type="password" bind:value={key} onchange={() => setGatewayKey(key.trim())} autocomplete="off" />
              </div>
            </Field>
          {/if}
          <p class="truncate font-mono text-xs text-fg-faint" title={endpoint}>{endpoint}</p>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
    <IconButton icon={Trash2} label="Clear" onclick={clear} disabled={!turns.length} />
  {/if}
</PageHeader>

{#if loading}
  <div class="h-[calc(100vh-11rem)] min-h-[28rem]" aria-busy="true"></div>
{:else if ready.length === 0 && !asked}
  <Empty icon={MessageSquare} title="Nothing is serving">
    {#if live.models.size}<Button variant="primary" href="/store">Library</Button>{:else}<Button variant="primary" href="/catalog">Discover models</Button>{/if}
  </Empty>
{:else}
  <section class="flex h-[calc(100vh-11rem)] min-h-[28rem] flex-col">
    {#if chosen && !chosenReady}
      <div class="note note-warn mb-3">{chosen.name} is {enumLabel(RouteState, chosen.state)}</div>
    {/if}
    <div bind:this={log} class="min-h-0 flex-1 overflow-y-auto">
      <div class="mx-auto flex max-w-3xl flex-col gap-6 py-2">
        {#if turns.length === 0}
          <div class="flex flex-1 flex-col items-center justify-center gap-3 py-24 text-center">
            <Logo size={40} class="text-fg-faint" />
            <div class="font-mono text-sm text-fg-muted">{chosen?.name}</div>
          </div>
        {/if}
        {#each turns as t, i (i)}
          {#if t.role === 'user'}
            <div class="flex justify-end">
              <div class="max-w-[85%] rounded-xl rounded-br-sm bg-raised px-4 py-2.5 text-sm whitespace-pre-wrap text-fg">{t.text}</div>
            </div>
          {:else}
            <div class="flex gap-3">
              <Logo size={18} class="mt-1 shrink-0 text-fg-faint" />
              <div class="min-w-0 flex-1">
                {#if t.text}
                  <Markdown markdown={t.text} />
                {:else if !t.error}
                  <span class="text-fg-faint">…</span>
                {/if}
                {#if t.error}
                  <div class="note note-bad {t.text ? 'mt-3' : ''}">{t.error}</div>
                {/if}
                {#if t.seconds !== undefined}
                  <div class="mt-1.5 inline-flex items-center gap-1.5 text-xs tabular-nums text-fg-faint">
                    {t.tokens ?? 0} tokens · {t.seconds.toFixed(1)}s{t.seconds && t.tokens ? ` · ${(t.tokens / t.seconds).toFixed(1)} tok/s` : ''}
                    {#if t.text}<Copy text={t.text} size={12} class="h-6 w-6" />{/if}
                  </div>
                {/if}
              </div>
            </div>
          {/if}
        {/each}
      </div>
    </div>
    <div class="pt-3">
      <div class="mx-auto flex max-w-3xl items-end gap-2 rounded-xl border border-line bg-sunken p-1.5 pl-3 transition-colors focus-within:border-accent">
        <textarea bind:this={box} class="max-h-[200px] min-h-9 flex-1 resize-none bg-transparent py-2 text-sm text-fg placeholder:text-fg-faint focus:outline-none" rows="1" bind:value={draft} onkeydown={onKey} oninput={grow} placeholder="Message {chosen?.name ?? ''}"></textarea>
        {#if busy}
          <Button variant="danger" icon={Square} onclick={stop} aria-label="Stop" />
        {:else}
          <Button variant="primary" icon={ArrowUp} onclick={send} disabled={!draft.trim() || !chosenReady} aria-label="Send" />
        {/if}
      </div>
    </div>
  </section>
{/if}
