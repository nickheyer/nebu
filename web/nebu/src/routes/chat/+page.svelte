<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { page } from '$app/state';
  import { api, baseUrl, gatewayKey, setGatewayKey } from '$lib/api';
  import { listenerUrl } from '$lib/gateway';
  import { live } from '$lib/state.svelte';
  import { fail } from '$lib/toast.svelte';
  import { RouteState, type GatewayStatus } from '$proto/gateway_pb';
  import { MessageSquare, Send, Square, Trash2, KeyRound } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Markdown from '$lib/components/ui/Markdown.svelte';

  interface Turn {
    role: 'user' | 'assistant';
    text: string;
    tokens?: number;
    seconds?: number;
  }

  let status = $state<GatewayStatus | null>(null);
  let model = $state('');
  let system = $state('');
  let temperature = $state('');
  let maxTokens = $state('');
  let draft = $state('');
  let turns = $state<Turn[]>([]);
  let busy = $state(false);
  let key = $state(gatewayKey());
  let log: HTMLDivElement | undefined = $state();
  let controller: AbortController | null = null;

  const ready = $derived([...live.routes.values()].filter((r) => r.state === RouteState.READY).sort((a, b) => a.name.localeCompare(b.name)));
  const chosen = $derived(ready.find((r) => r.name === model) ?? ready[0]);
  // The select follows the route the chat really targets when the one asked for is not ready
  $effect(() => {
    if (chosen && model !== chosen.name) model = chosen.name;
  });
  // The gateway on the API listener is same origin, one of its own is reached by address
  const endpoint = $derived.by(() => {
    const own = status?.listeners.find((l) => !l.shared);
    return (own ? listenerUrl(own.addr, !!status?.tls) : baseUrl) + '/v1/chat/completions';
  });

  onMount(async () => {
    model = page.url.searchParams.get('model') ?? '';
    try {
      status = (await api.gateway.getGatewayStatus({})).status ?? null;
    } catch (err) {
      fail(err, 'Gateway status failed');
    }
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

  // Sends the conversation and streams the answer into the last turn
  async function send() {
    const text = draft.trim();
    if (!text || busy || !chosen) return;
    draft = '';
    turns = [...turns, { role: 'user', text }, { role: 'assistant', text: '' }];
    busy = true;
    controller = new AbortController();
    const started = performance.now();
    let tokens = 0;
    scroll();
    try {
      const messages = [...(system.trim() ? [{ role: 'system', content: system.trim() }] : []), ...turns.slice(0, -1).map((t) => ({ role: t.role, content: t.text }))];
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
          if (!line.startsWith('data:')) continue;
          const data = line.slice(5).trim();
          if (data === '[DONE]') continue;
          try {
            const chunk = JSON.parse(data);
            const delta = chunk.choices?.[0]?.delta?.content ?? chunk.choices?.[0]?.text ?? '';
            if (delta) {
              turns[turns.length - 1].text += delta;
              tokens++;
              scroll();
            }
            if (chunk.usage?.completion_tokens) tokens = chunk.usage.completion_tokens;
          } catch {
            // a partial or foreign line
          }
        }
      }
    } catch (err) {
      if (!(err instanceof DOMException && err.name === 'AbortError')) {
        turns[turns.length - 1].text += `\n\n> ${err instanceof Error ? err.message : String(err)}`;
      }
    } finally {
      const last = turns[turns.length - 1];
      last.tokens = tokens;
      last.seconds = (performance.now() - started) / 1000;
      busy = false;
      controller = null;
      scroll();
    }
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      send();
    }
  }
</script>

<PageHeader title="Chat" description="Talk to a running model through the gateway, the same path your clients take">
  {#if turns.length}<Button variant="outline" icon={Trash2} onclick={clear}>Clear</Button>{/if}
</PageHeader>

{#if ready.length === 0}
  <div class="panel"><Empty icon={MessageSquare} title="Nothing is serving" description="Run a stored model, or drop one on a slot, and it shows up here as soon as its route is ready." /></div>
{:else}
  <div class="grid grid-cols-1 gap-4 lg:grid-cols-[18rem_1fr]">
    <aside class="panel flex flex-col gap-4 p-4">
      <Field label="Model" for="chat-model" hint="Routes that answer right now">
        <select id="chat-model" class="input font-mono" bind:value={model}>
          {#each ready as r (r.name)}<option value={r.name}>{r.name}</option>{/each}
        </select>
      </Field>
      <Field label="System prompt" for="chat-system">
        <textarea id="chat-system" class="input h-24" bind:value={system} placeholder="You are a terse assistant."></textarea>
      </Field>
      <div class="grid grid-cols-2 gap-3">
        <Field label="Temperature" for="chat-temp">
          <input id="chat-temp" class="input font-mono" inputmode="decimal" bind:value={temperature} placeholder="runtime" />
        </Field>
        <Field label="Max tokens" for="chat-max">
          <input id="chat-max" class="input font-mono" inputmode="numeric" bind:value={maxTokens} placeholder="runtime" />
        </Field>
      </div>
      {#if status?.auth}
        <Field label="Gateway key" for="chat-key" hint="gateway.api_keys is set, so the chat needs one. Kept in this browser only">
          <div class="relative">
            <KeyRound size={14} class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-fg-faint" />
            <input id="chat-key" class="input pl-9 font-mono" type="password" bind:value={key} onchange={() => setGatewayKey(key.trim())} autocomplete="off" />
          </div>
        </Field>
      {/if}
      <p class="text-[11px] leading-4 text-fg-faint">Sent to <span class="font-mono">{endpoint}</span> as <span class="font-mono">{chosen?.name}</span>{chosen?.model ? `, serving ${chosen.model}` : ''}.</p>
    </aside>

    <section class="panel flex min-h-[32rem] flex-col">
      <div bind:this={log} class="flex-1 overflow-y-auto p-4">
        {#if turns.length === 0}
          <p class="text-sm text-fg-faint">Ask something. Answers stream in as the runtime produces them, and each one says how many tokens it took and how long.</p>
        {/if}
        <div class="flex flex-col gap-4">
          {#each turns as t, i (i)}
            <div class="flex flex-col gap-1 {t.role === 'user' ? 'items-end' : 'items-start'}">
              <div class="max-w-[85%] rounded-lg px-3 py-2 text-sm {t.role === 'user' ? 'bg-accent/12 text-fg' : 'border border-line bg-sunken text-fg'}">
                {#if t.role === 'user'}
                  <span class="whitespace-pre-wrap">{t.text}</span>
                {:else if t.text}
                  <Markdown markdown={t.text} />
                {:else}
                  <span class="text-fg-faint">…</span>
                {/if}
              </div>
              {#if t.role === 'assistant' && t.seconds !== undefined}
                <span class="text-[11px] text-fg-faint">{t.tokens ?? 0} tokens in {t.seconds.toFixed(1)}s{t.seconds && t.tokens ? `, ${(t.tokens / t.seconds).toFixed(1)} tok/s` : ''}</span>
              {/if}
            </div>
          {/each}
        </div>
      </div>
      <div class="flex items-end gap-2 border-t border-line p-3">
        <textarea class="input min-h-10 flex-1 resize-none" rows="2" bind:value={draft} onkeydown={onKey} placeholder="Message, Enter to send, Shift+Enter for a new line" disabled={busy && false}></textarea>
        {#if busy}
          <Button variant="danger" icon={Square} onclick={stop}>Stop</Button>
        {:else}
          <Button variant="primary" icon={Send} onclick={send} disabled={!draft.trim() || !chosen}>Send</Button>
        {/if}
      </div>
    </section>
  </div>
{/if}
