<script lang="ts">
  import { onMount, tick, untrack } from 'svelte';
  import { page } from '$app/state';
  import { replaceState } from '$app/navigation';
  import { baseUrl, gatewayKey, setGatewayKey } from '$lib/api';
  import { listenerUrl } from '$lib/gateway';
  import { live, cached, slotByRef, instanceLive, runtimeName, clock } from '$lib/state.svelte';
  import { readLocal, writeLocal } from '$lib/persist';
  import { byName, enumLabel, ms, rate, duration, millisBetween } from '$lib/format';
  import { fail } from '$lib/toast.svelte';
  import { send, countTokens, type ChatMessage, type Dialect, type Sampling, type ToolCall, type Sent } from '$lib/chatClient';
  import { RouteState } from '$proto/gateway_pb';
  import { MessageSquare, Square, Trash2, ArrowUp, RotateCcw, PanelRightClose, PanelRightOpen, Wrench, X, FileJson } from '@lucide/svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import Select from '$lib/components/ui/Select.svelte';
  import Segmented from '$lib/components/ui/Segmented.svelte';
  import Switch from '$lib/components/ui/Switch.svelte';
  import NumberInput from '$lib/components/ui/NumberInput.svelte';
  import TextInput from '$lib/components/ui/TextInput.svelte';
  import TextArea from '$lib/components/ui/TextArea.svelte';
  import Tabs from '$lib/components/ui/Tabs.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Markdown from '$lib/components/ui/Markdown.svelte';
  import Copy from '$lib/components/ui/Copy.svelte';
  import Json from '$lib/components/ui/Json.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Logo from '$lib/components/Logo.svelte';
  import InstanceLog from '$lib/components/InstanceLog.svelte';
  import TraceDetail from '$lib/components/TraceDetail.svelte';

  // One turn of the conversation with everything measured about it
  interface Turn {
    role: 'user' | 'assistant';
    text: string;
    toolCalls?: ToolCall[];
    error?: string;
    // The trace the gateway kept for the answer
    trace?: string;
    // Measured in the browser
    startedAt?: number;
    firstTokenAt?: number;
    finishedAt?: number;
    promptTokens?: number;
    completionTokens?: number;
    stop?: string;
    dialect?: Dialect;
  }

  // What is kept per model name in this browser
  interface Session {
    turns: Turn[];
    system: string;
    dialect: Dialect;
    stream: boolean;
    temperature: string;
    topP: string;
    topK: string;
    maxTokens: string;
    stop: string;
    seed: string;
    tools: string;
  }

  const blank = (): Session => ({ turns: [], system: '', dialect: 'openai', stream: true, temperature: '', topP: '', topK: '', maxTokens: '', stop: '', seed: '', tools: '' });

  let model = $state('');
  let session = $state<Session>(blank());
  let draft = $state('');
  let busy = $state(false);
  let key = $state(gatewayKey());
  let inspector = $state(true);
  let pane = $state('settings');
  let shownTrace = $state('');
  let lastSent = $state<Sent | null>(null);
  let promptTokens = $state<number | null>(null);
  let countError = $state('');
  let log: HTMLDivElement | undefined = $state();
  let box: HTMLTextAreaElement | undefined = $state();
  let controller: AbortController | null = null;
  let countController: AbortController | null = null;

  const loading = $derived(!live.ready && !live.error);
  const status = $derived(cached.gateway);
  const routes = $derived([...live.routes.values()].sort(byName((r) => r.name)));
  const ready = $derived(routes.filter((r) => r.state === RouteState.READY));
  // The name asked for stays chosen while it exists, else the first that answers
  const asked = $derived(model ? live.routes.get(model) : undefined);
  const chosen = $derived(asked ?? ready[0]);
  // Primitives of the chosen route, so effects re-run only when these change and not on every counter tick
  const chosenName = $derived(chosen?.name ?? '');
  const chosenReady = $derived(chosen?.state === RouteState.READY);
  $effect(() => {
    if (!asked && chosen && model !== chosen.name) model = chosen.name;
  });
  const instance = $derived(chosen?.instanceId ? live.instances.get(chosen.instanceId) : undefined);
  const slot = $derived(chosen?.slotId ? slotByRef(chosen.slotId) : undefined);
  const install = $derived(instance?.installId ? live.installs.get(instance.installId) : undefined);
  // The gateway on the API listener is same origin, one of its own is reached by address
  const own = $derived(status?.listeners.find((l) => !l.shared));
  const gatewayBase = $derived(own ? listenerUrl(own.addr, !!status?.tls) : baseUrl);
  const modelItems = $derived(routes.map((r) => ({ value: r.name, label: r.name, detail: r.state === RouteState.READY ? undefined : enumLabel(RouteState, r.state) })));
  const toolsProblem = $derived.by(() => {
    if (!session.tools.trim()) return '';
    try {
      const parsed = JSON.parse(session.tools);
      return Array.isArray(parsed) ? '' : 'Must be a JSON array of tools';
    } catch (err) {
      return err instanceof Error ? err.message : 'Not valid JSON';
    }
  });
  const tools = $derived(toolsProblem || !session.tools.trim() ? [] : (JSON.parse(session.tools) as unknown[]));
  const sampling = $derived.by((): Sampling => {
    const num = (s: string) => (s.trim() === '' ? undefined : parseFloat(s));
    const int = (s: string) => (s.trim() === '' ? undefined : parseInt(s, 10));
    const stops = session.stop
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    return { temperature: num(session.temperature), topP: num(session.topP), topK: int(session.topK), maxTokens: int(session.maxTokens), seed: int(session.seed), stop: stops.length ? stops : undefined };
  });
  const lastAnswer = $derived([...session.turns].reverse().find((t) => t.role === 'assistant'));
  const inspectorTabs = $derived([
    { id: 'settings', label: 'Settings' },
    { id: 'request', label: 'Request' },
    { id: 'trace', label: 'Trace' },
    { id: 'log', label: 'Log' }
  ]);

  // Sessions live per model name in this browser
  const sessionKey = (name: string) => `nebu.chat.${name}`;
  let loaded = '';
  $effect(() => {
    const name = chosenName;
    untrack(() => {
      if (!name || name === loaded) return;
      loaded = name;
      try {
        const raw = readLocal(sessionKey(name));
        session = raw ? { ...blank(), ...(JSON.parse(raw) as Partial<Session>) } : blank();
      } catch {
        session = blank();
      }
      shownTrace = lastAnswer?.trace ?? '';
    });
  });
  let saveTimer: ReturnType<typeof setTimeout> | null = null;
  $effect(() => {
    const snapshot = JSON.stringify(session);
    const name = loaded;
    if (!name) return;
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = setTimeout(() => writeLocal(sessionKey(name), snapshot), 300);
  });

  onMount(() => {
    model = page.url.searchParams.get('model') ?? '';
  });
  $effect(() => {
    const name = chosenName;
    if (!name || page.url.searchParams.get('model') === name) return;
    const url = new URL(page.url);
    url.searchParams.set('model', name);
    replaceState(url, {});
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
    session.turns = [];
    lastSent = null;
    shownTrace = '';
    box?.focus();
  }

  function removeTurn(i: number) {
    session.turns = session.turns.filter((_, j) => j !== i);
  }

  // The history the model sees: answered turns only, an unanswered question left out
  function history(upTo = session.turns.length): ChatMessage[] {
    const out: ChatMessage[] = [];
    const past = session.turns.slice(0, upTo);
    for (let i = 0; i < past.length; i++) {
      const t = past[i];
      if (t.role !== 'user') continue;
      const answer = past[i + 1];
      if (answer?.role === 'assistant' && (answer.text || answer.toolCalls?.length) && !answer.error) {
        out.push({ role: 'user', content: t.text });
        out.push({ role: 'assistant', content: answer.text, toolCalls: answer.toolCalls?.length ? answer.toolCalls : undefined });
      } else if (i === past.length - 1) {
        out.push({ role: 'user', content: t.text });
      }
    }
    return out;
  }

  // Says what went wrong reaching the gateway, a blocked origin or an untrusted certificate being the usual causes
  function explain(err: unknown): string {
    const text = err instanceof Error ? err.message : String(err);
    if (err instanceof TypeError && own) {
      return `${text}. Could not reach ${gatewayBase}. Open ${gatewayBase}/health once to accept its certificate, or add ${window.location.origin} to gateway.cors_origins.`;
    }
    return text;
  }

  // Sends the conversation up to the given user turn and streams the answer into the turn after it
  async function ask(userIndex: number) {
    if (busy || !chosen || !chosenReady) return;
    const messages = history(userIndex + 1);
    session.turns = [...session.turns.slice(0, userIndex + 1), { role: 'assistant', text: '', dialect: session.dialect, startedAt: Date.now() }];
    const turn = session.turns[session.turns.length - 1];
    busy = true;
    controller = new AbortController();
    const calls = new Map<number, ToolCall>();
    scroll();
    try {
      await send(
        { base: gatewayBase, dialect: session.dialect, model: chosen.name, messages, system: session.system.trim(), sampling, stream: session.stream, tools, key, signal: controller.signal },
        (ev) => {
          switch (ev.kind) {
            case 'text':
              if (!turn.firstTokenAt) turn.firstTokenAt = Date.now();
              turn.text += ev.text ?? '';
              scroll();
              break;
            case 'tool': {
              if (!turn.firstTokenAt) turn.firstTokenAt = Date.now();
              const t = ev.tool!;
              const call = calls.get(t.index) ?? { id: '', name: '', arguments: '' };
              if (t.id) call.id = t.id;
              if (t.name) call.name = t.name;
              call.arguments += t.arguments ?? '';
              calls.set(t.index, call);
              turn.toolCalls = [...calls.values()];
              break;
            }
            case 'usage':
              if (ev.promptTokens) turn.promptTokens = ev.promptTokens;
              if (ev.completionTokens) turn.completionTokens = ev.completionTokens;
              break;
            case 'stop':
              turn.stop = ev.stop;
              break;
            case 'error':
              turn.error = ev.error;
              break;
          }
        },
        (sent) => {
          lastSent = sent;
          turn.trace = sent.trace;
          shownTrace = sent.trace;
        }
      );
    } catch (err) {
      if (!(err instanceof DOMException && err.name === 'AbortError')) {
        turn.error = explain(err);
        fail(err, 'Request failed');
      } else {
        turn.stop = 'cancelled';
      }
    } finally {
      turn.finishedAt = Date.now();
      busy = false;
      controller = null;
      scroll();
      box?.focus();
    }
  }

  function submit() {
    const text = draft.trim();
    if (!text || busy || !chosen || !chosenReady) return;
    draft = '';
    if (box) box.style.height = '';
    session.turns = [...session.turns, { role: 'user', text }];
    ask(session.turns.length - 1);
  }

  // Asks again from the last question, dropping the last answer
  function regenerate() {
    const i = session.turns.length - 1;
    if (i < 0) return;
    const userIndex = session.turns[i].role === 'assistant' ? i - 1 : i;
    if (userIndex < 0 || session.turns[userIndex].role !== 'user') return;
    ask(userIndex);
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      submit();
    }
  }

  // The box grows with the draft up to a few lines
  function grow() {
    if (!box) return;
    box.style.height = '';
    box.style.height = Math.min(box.scrollHeight, 200) + 'px';
  }

  // The prompt's size as the gateway counts it, read once typing settles; only primitives are read so a route's counter ticks do not recount
  $effect(() => {
    const text = draft;
    const sys = session.system;
    const turnsSnapshot = session.turns.length;
    const name = chosenName;
    const isReady = chosenReady;
    void turnsSnapshot;
    countController?.abort();
    if (!name || !isReady) {
      promptTokens = null;
      return;
    }
    const c = new AbortController();
    countController = c;
    const timer = setTimeout(async () => {
      try {
        const messages = [...history(), ...(text.trim() ? [{ role: 'user' as const, content: text.trim() }] : [])];
        if (messages.length === 0 && !sys.trim()) {
          promptTokens = null;
          return;
        }
        promptTokens = await countTokens(gatewayBase, name, sys.trim(), messages, key, c.signal);
        countError = '';
      } catch (err) {
        if (!c.signal.aborted) {
          promptTokens = null;
          countError = err instanceof Error ? err.message : String(err);
        }
      }
    }, 500);
    return () => {
      clearTimeout(timer);
      c.abort();
    };
  });

  const stopWord = (s: string | undefined) => (s === 'end_turn' ? 'stop' : s === 'max_tokens' ? 'length' : s === 'tool_calls' || s === 'tool_use' ? 'tool' : s);
  // Why an answer ended, said only when it is worth saying: a normal finish needs no note
  function stopNote(s: string | undefined): string {
    const w = stopWord(s);
    if (!w || w === 'stop') return '';
    if (w === 'length') return 'cut off at max tokens';
    if (w === 'tool') return 'stopped to call a tool';
    return `stopped: ${w}`;
  }
</script>

<svelte:head><title>Chat · nebu</title></svelte:head>

{#if loading}
  <div class="h-[calc(100vh-6rem)]" aria-busy="true"></div>
{:else if routes.length === 0}
  <Empty icon={MessageSquare} title="Nothing is serving. Run a model to chat with it.">
    {#if live.models.size}<Button variant="primary" href="/store">Library</Button>{:else}<Button variant="primary" href="/catalog">Browse catalog</Button>{/if}
  </Empty>
{:else}
  <div class="flex h-[calc(100vh-4.5rem)] min-h-[32rem] gap-4 lg:h-[calc(100vh-2.5rem)]">
    <section class="card flex min-w-0 flex-1 flex-col">
      <header class="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-line px-4 py-2.5">
        <Select class="w-80 max-w-full" mono bind:value={model} label="Model" items={modelItems} />
        {#if chosen}
          <State values={RouteState} value={chosen.state} />
          {#if instance}
            <span class="hidden items-center gap-x-3 text-xs text-fg-muted md:flex">
              <span class="kv"><span>runtime</span><span>{runtimeName(instance.runtimeId)}{install?.version ? ` ${install.version}` : ''}</span></span>
              {#if instance.params.n_ctx}<span class="kv"><span>ctx</span><span>{instance.params.n_ctx}</span></span>{/if}
              {#if slot}<a class="kv link" href="/slots/{slot.id}"><span>slot</span><span>{slot.name}</span></a>{:else}<a class="link" href="/instances/{instance.id}">Open instance</a>{/if}
              {#if instanceLive(instance)}<span class="kv"><span>up</span><span>{duration(instance.readyAt ?? instance.createdAt, undefined, clock.now)}</span></span>{/if}
            </span>
          {/if}
        {/if}
        <span class="ml-auto flex items-center gap-1">
          <IconButton icon={RotateCcw} label="Regenerate the last answer" onclick={regenerate} disabled={busy || !session.turns.length} />
          <IconButton icon={Trash2} label="Clear the conversation" onclick={clear} disabled={!session.turns.length} />
          <IconButton icon={inspector ? PanelRightClose : PanelRightOpen} label={inspector ? 'Hide inspector' : 'Show inspector'} onclick={() => (inspector = !inspector)} />
        </span>
      </header>

      {#if chosen && !chosenReady}
        <div class="note note-warn m-4 mb-0">{chosen.name} is {enumLabel(RouteState, chosen.state)}. Requests will wait until it answers.</div>
      {/if}

      <div bind:this={log} class="min-h-0 flex-1 overflow-y-auto px-4">
        <div class="mx-auto flex max-w-3xl flex-col gap-5 py-4">
          {#if session.turns.length === 0}
            <div class="flex flex-col items-center justify-center gap-3 py-20 text-center">
              <Logo size={36} class="text-fg-faint" />
              <div class="font-mono text-sm text-fg-muted">{chosen?.name}</div>
              <p class="max-w-sm text-xs leading-5 text-fg-faint">Every answer records its timing, token counts, and the exact request. Open the inspector to see them.</p>
            </div>
          {/if}
          {#each session.turns as t, i (i)}
            {#if t.role === 'user'}
              <div class="group flex items-start justify-end gap-2">
                <button type="button" class="mt-2 rounded-md p-1 text-fg-faint opacity-0 transition-opacity group-hover:opacity-100 hover:bg-raised hover:text-fg" aria-label="Remove" onclick={() => removeTurn(i)}><X size={13} /></button>
                <div class="max-w-[85%] rounded-xl rounded-br-sm bg-raised px-4 py-2.5 text-sm whitespace-pre-wrap text-fg">{t.text}</div>
              </div>
            {:else}
              {@const ttft = t.startedAt && t.firstTokenAt ? t.firstTokenAt - t.startedAt : undefined}
              {@const total = t.startedAt && t.finishedAt ? t.finishedAt - t.startedAt : undefined}
              {@const gen = t.firstTokenAt && t.finishedAt ? t.finishedAt - t.firstTokenAt : undefined}
              {@const trace = t.trace ? live.traces.get(t.trace) : undefined}
              <div class="flex gap-3">
                <Logo size={18} class="mt-1 shrink-0 text-fg-faint" />
                <div class="min-w-0 flex-1">
                  {#if t.text}
                    <Markdown markdown={t.text} />
                  {:else if !t.error && !t.toolCalls?.length}
                    <span class="text-fg-faint">{t.finishedAt ? 'Empty answer' : '…'}</span>
                  {/if}
                  {#each t.toolCalls ?? [] as c, ci (ci)}
                    <div class="card mt-2 p-3">
                      <div class="mb-2 flex items-center gap-2 text-xs"><Wrench size={12} class="text-accent" /><span class="font-mono text-fg">{c.name || 'tool'}</span>{#if c.id}<span class="font-mono text-fg-faint">{c.id}</span>{/if}</div>
                      <Json text={c.arguments} height="max-h-48" empty="No arguments" />
                    </div>
                  {/each}
                  {#if t.error}
                    <div class="note note-bad {t.text ? 'mt-3' : ''}">{t.error}</div>
                  {/if}
                  {#if t.finishedAt}
                    <div class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs tabular-nums text-fg-faint">
                      <span title="Time to first token">{ms(ttft)} first token</span>
                      <span title="Whole request">{ms(total)} total</span>
                      {#if t.promptTokens || trace?.promptTokens}<span>{t.promptTokens || trace?.promptTokens} in</span>{/if}
                      {#if t.completionTokens || trace?.completionTokens}<span>{t.completionTokens || trace?.completionTokens} out</span>{/if}
                      <span>{rate(t.completionTokens || trace?.completionTokens, gen)}</span>
                      {#if stopNote(t.stop || trace?.stop)}<span class="text-warn">{stopNote(t.stop || trace?.stop)}</span>{/if}
                      {#if t.dialect}<span class="font-mono">{t.dialect}</span>{/if}
                      {#if trace && trace.firstTokenAt}<span title="As measured by the gateway">gateway {ms(millisBetween(trace.startedAt, trace.firstTokenAt))} · {ms(millisBetween(trace.startedAt, trace.finishedAt))}</span>{/if}
                      {#if t.trace}
                        <button type="button" class="inline-flex items-center gap-1 text-fg-muted hover:text-fg" onclick={() => { shownTrace = t.trace!; pane = 'trace'; inspector = true; }}><FileJson size={12} /> trace</button>
                      {/if}
                      {#if t.text}<Copy text={t.text} size={12} class="h-6 w-6" />{/if}
                    </div>
                  {/if}
                </div>
              </div>
            {/if}
          {/each}
        </div>
      </div>

      <div class="border-t border-line p-3">
        <div class="mx-auto flex max-w-3xl flex-col gap-1.5">
          <div class="flex items-end gap-2 rounded-xl border border-line bg-sunken p-1.5 pl-3 transition-colors focus-within:border-accent">
            <div class="relative min-w-0 flex-1">
              <textarea bind:this={box} class="max-h-[200px] min-h-9 w-full resize-none bg-transparent py-2 text-sm text-fg focus:outline-none" rows="1" bind:value={draft} onkeydown={onKey} oninput={grow}></textarea>
              {#if !draft}<span class="pointer-events-none absolute top-2 left-0 text-sm text-fg-faint" aria-hidden="true">{chosen ? `Message ${chosen.name}` : 'Pick a model'}</span>{/if}
            </div>
            {#if busy}
              <Button variant="danger" icon={Square} onclick={stop} aria-label="Stop" />
            {:else}
              <Button variant="primary" icon={ArrowUp} onclick={submit} disabled={!draft.trim() || !chosenReady} aria-label="Send" />
            {/if}
          </div>
          <div class="flex flex-wrap items-center gap-x-3 px-1 text-xs text-fg-faint">
            <span class="font-mono">{session.dialect}{session.stream ? ' · stream' : ''}</span>
            {#if promptTokens !== null}<span title="Counted by the gateway, through the runtime's tokenizer when it has one">{promptTokens} prompt tokens{instance?.params.n_ctx ? ` of ${instance.params.n_ctx}` : ''}</span>{:else if countError}<span class="text-warn" title={countError}>token count unavailable</span>{/if}
            <span class="ml-auto">Enter sends, Shift+Enter for a new line</span>
          </div>
        </div>
      </div>
    </section>

    {#if inspector}
      <aside class="card flex w-[24rem] shrink-0 flex-col">
        <Tabs size="sm" bind:value={pane} tabs={inspectorTabs} class="px-3 pt-1" />
        <div class="min-h-0 flex-1 overflow-y-auto p-4">
          {#if pane === 'settings'}
            <div class="flex flex-col gap-5">
              <Field label="Wire format" description="The format the request is written in. The gateway translates to what the runtime speaks.">
                <Segmented bind:value={session.dialect} tabs={[{ id: 'openai', label: 'OpenAI' }, { id: 'anthropic', label: 'Anthropic' }, { id: 'ollama', label: 'Ollama' }]} />
              </Field>
              <div class="flex items-center justify-between gap-4 rounded-md border border-line px-3 py-2.5">
                <div><div class="text-[13px] font-medium text-fg">Stream</div><div class="text-xs text-fg-muted">Tokens arrive as they are generated.</div></div>
                <Switch bind:checked={session.stream} label="Stream" />
              </div>
              <Field label="System prompt" for="chat-system">
                <TextArea id="chat-system" bind:value={session.system} />
              </Field>
              <div class="grid grid-cols-2 gap-3">
                <Field label="Temperature" for="chat-temp"><NumberInput id="chat-temp" min={0} max={2} step={0.1} bind:value={session.temperature} /></Field>
                <Field label="Top P" for="chat-topp"><NumberInput id="chat-topp" min={0} max={1} step={0.05} bind:value={session.topP} /></Field>
                <Field label="Top K" for="chat-topk"><NumberInput id="chat-topk" integer min={0} bind:value={session.topK} /></Field>
                <Field label="Max tokens" for="chat-max"><NumberInput id="chat-max" integer min={1} step={64} bind:value={session.maxTokens} /></Field>
                <Field label="Seed" for="chat-seed" description="Same seed, same sampling."><NumberInput id="chat-seed" integer min={0} bind:value={session.seed} /></Field>
                <Field label="Stop sequences" for="chat-stop" description="Comma separated."><TextInput id="chat-stop" mono bind:value={session.stop} empty="###, User:" /></Field>
              </div>
              <Field label="Tools" for="chat-tools" description="A JSON array of tool definitions in the OpenAI shape. The gateway translates them for other formats." error={toolsProblem || undefined}>
                <TextArea id="chat-tools" mono height="h-36" bind:value={session.tools} empty={'[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]'} invalid={!!toolsProblem} />
              </Field>
              {#if status?.auth}
                <Field label="API key" for="chat-key" description="Stored in this browser.">
                  <TextInput id="chat-key" mono type="password" bind:value={key} onchange={() => setGatewayKey(key.trim())} />
                </Field>
              {/if}
              <p class="truncate font-mono text-xs text-fg-faint" title={gatewayBase}>{gatewayBase}</p>
            </div>
          {:else if pane === 'request'}
            {#if lastSent}
              <div class="flex flex-col gap-4">
                <div class="grid grid-cols-2 gap-3">
                  <div class="stat"><dt>Path</dt><dd class="font-mono">{lastSent.path}</dd></div>
                  <div class="stat"><dt>Status</dt><dd>{lastSent.status}</dd></div>
                </div>
                <div>
                  <div class="caps mb-2 text-fg-faint">Headers</div>
                  <pre class="code whitespace-pre-wrap">{Object.entries(lastSent.headers).map(([k, v]) => `${k}: ${k.toLowerCase() === 'authorization' || k.toLowerCase() === 'x-api-key' ? '••••' : v}`).join('\n')}</pre>
                </div>
                <div>
                  <div class="caps mb-2 text-fg-faint">Body</div>
                  <Json text={lastSent.body} height="max-h-[32rem]" />
                </div>
              </div>
            {:else}
              <p class="text-sm text-fg-faint">The last request sent from this page appears here.</p>
            {/if}
          {:else if pane === 'trace'}
            {#if shownTrace}
              <TraceDetail id={shownTrace} />
            {:else}
              <p class="text-sm text-fg-faint">Send a message. The gateway's record of it appears here: timing, tokens, and what the runtime received.</p>
            {/if}
          {:else if pane === 'log'}
            {#if instance}
              {#key instance.id}<InstanceLog id={instance.id} follow={instanceLive(instance)} height="h-[calc(100vh-14rem)]" />{/key}
            {:else}
              <p class="text-sm text-fg-faint">No instance behind this name yet.</p>
            {/if}
          {/if}
        </div>
      </aside>
    {/if}
  </div>
{/if}
