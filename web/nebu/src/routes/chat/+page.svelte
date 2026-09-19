<script lang="ts">
  import { onDestroy, onMount, tick, untrack } from 'svelte';
  import { SvelteMap, SvelteSet } from 'svelte/reactivity';
  import { page } from '$app/state';
  import { replaceState } from '$app/navigation';
  import { baseUrl, gatewayKey, setGatewayKey } from '$lib/api';
  import { listenerUrl } from '$lib/gateway';
  import { live, cached, slotByRef, instanceLive, runtimeName, modelKey, clock } from '$lib/state.svelte';
  import { readLocal, writeLocal } from '$lib/persist';
  import { byName, bytes, enumLabel, ms, rate, duration, millisBetween } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { send, countTokens, type ChatMessage, type Dialect, type Sampling, type ToolCall, type Sent } from '$lib/chatClient';
  import { capabilities, createVideo, deleteVideo, fetchVideo, figure, generateImages, getVideo, lorasText, modeKey, namedChoice, parseLoras, readHistory, writeHistory, type Capabilities, type Generation, type MediaRequest } from '$lib/generate';
  import { prepareImage, storeImage, storeBlob, fetchImage, putImage, getImage, deleteImages, toBase64, newId, type Attachment } from '$lib/images';
  import { RouteState } from '$proto/gateway_pb';
  import { ApiFlavor } from '$proto/runtime_pb';
  import { ArtifactRole, TensorGroupKind } from '$proto/model_pb';
  import { MessageSquare, Square, Trash2, ArrowUp, RotateCcw, PanelRightClose, PanelRightOpen, Wrench, X, FileJson, ImagePlus, ImageOff, Download, Film, Settings2 } from '@lucide/svelte';
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
  import Spinner from '$lib/components/ui/Spinner.svelte';
  import Lightbox from '$lib/components/ui/Lightbox.svelte';
  import Logo from '$lib/components/Logo.svelte';
  import InstanceLog from '$lib/components/InstanceLog.svelte';
  import TraceDetail from '$lib/components/TraceDetail.svelte';
  import MediaSettings, { type MediaForm } from '$lib/components/chat/MediaSettings.svelte';

  // Store inline image bytes by ID. Preserve remote image URLs.
  interface Media {
    key: string;
    id?: string;
    url?: string;
    error?: string;
  }

  interface Turn {
    role: 'user' | 'assistant';
    text: string;
    images?: Attachment[];
    media?: Media[];
    toolCalls?: ToolCall[];
    error?: string;
    trace?: string;
    startedAt?: number;
    firstTokenAt?: number;
    finishedAt?: number;
    promptTokens?: number;
    completionTokens?: number;
    stop?: string;
    dialect?: Dialect;
  }

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

  type Mode = 'image' | 'video';

  type Outgoing = ChatMessage & { attachments?: Attachment[] };

  const blank = (): Session => ({ turns: [], system: '', dialect: 'openai', stream: true, temperature: '', topP: '', topK: '', maxTokens: '', stop: '', seed: '', tools: '' });
  const blankForm = (): MediaForm => ({ negative: '', width: '1024', height: '1024', steps: '', cfg: '', seed: '', sampler: '', scheduler: '', n: '', frames: '', fps: '', strength: '', guidance: '', flowShift: '', clipSkip: '', vaeTiling: false, temporalTiling: false, highSteps: '', highCfg: '', format: '', loras: '' });

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

  let pending = $state<Attachment[]>([]);
  let attaching = $state(0);
  let dragDepth = $state(0);
  const dropping = $derived(dragDepth > 0);
  let picker: HTMLInputElement | undefined = $state();
  let lightbox = $state('');
  let lightboxAlt = $state('');
  const urls = new SvelteMap<string, string>();
  const missing = new SvelteSet<string>();
  const encoded = new Map<string, string>();
  const loadingIds = new Set<string>();

  let mode = $state<Mode>('image');
  let form = $state<MediaForm>(blankForm());
  let history = $state<Generation[]>([]);
  let caps = $state<Capabilities | null>(null);
  let capsError = $state('');

  const widthKey = 'nebu.chat.inspector';
  const minWidth = 320;
  let width = $state(384);
  let dragging = $state(false);
  const savedWidth = parseInt(readLocal(widthKey), 10);
  if (savedWidth >= minWidth) width = clampWidth(savedWidth);

  function clampWidth(w: number): number {
    return Math.min(Math.max(w, minWidth), Math.max(window.innerWidth * 0.6, minWidth));
  }

  function startDrag(e: PointerEvent) {
    e.preventDefault();
    dragging = true;
    const target = e.currentTarget as HTMLElement;
    const startX = e.clientX;
    const startWidth = width;
    target.setPointerCapture(e.pointerId);
    const move = (ev: PointerEvent) => {
      width = clampWidth(startWidth + (startX - ev.clientX));
    };
    const stop = () => {
      dragging = false;
      target.releasePointerCapture(e.pointerId);
      target.removeEventListener('pointermove', move);
      target.removeEventListener('pointerup', stop);
      target.removeEventListener('pointercancel', stop);
      writeLocal(widthKey, String(Math.round(width)));
    };
    target.addEventListener('pointermove', move);
    target.addEventListener('pointerup', stop);
    target.addEventListener('pointercancel', stop);
  }

  const loading = $derived(!live.ready && !live.error);
  const status = $derived(cached.gateway);
  const routes = $derived([...live.routes.values()].sort(byName((r) => r.name)));
  const ready = $derived(routes.filter((r) => r.state === RouteState.READY));
  const asked = $derived(model ? live.routes.get(model) : undefined);
  const chosen = $derived(asked ?? ready[0]);
  // Read primitive route fields so traffic counters do not retrigger effects.
  const chosenName = $derived(chosen?.name ?? '');
  const chosenReady = $derived(chosen?.state === RouteState.READY);
  const media = $derived(chosen?.api === ApiFlavor.SDCPP);
  $effect(() => {
    if (!asked && chosen && model !== chosen.name) model = chosen.name;
  });
  const instance = $derived(chosen?.instanceId ? live.instances.get(chosen.instanceId) : undefined);
  const slot = $derived(chosen?.slotId ? slotByRef(chosen.slotId) : undefined);
  const install = $derived(instance?.installId ? live.installs.get(instance.installId) : undefined);
  const stored = $derived(instance ? live.models.get(modelKey(instance)) : undefined);
  // Vision requires a projector or vision tensors. Unknown for models absent from the store.
  const vision = $derived.by((): boolean | undefined => {
    if (media) return true;
    if (!stored) return undefined;
    return stored.artifacts.some((a) => a.artifact?.role === ArtifactRole.PROJECTOR) || (stored.descriptor?.groups ?? []).some((g) => g.kind === TensorGroupKind.VISION);
  });
  const blind = $derived(vision === false);
  const blindNote = $derived(`${chosenName} has no vision encoder, so it cannot take images`);
  // Shared listeners use the page origin. Dedicated listeners use their address.
  const own = $derived(status?.listeners.find((l) => !l.shared));
  const gatewayBase = $derived(own ? listenerUrl(own.addr, !!status?.tls) : baseUrl);
  const modelItems = $derived(routes.map((r) => ({ value: r.name, label: r.name, detail: r.state === RouteState.READY ? (r.api === ApiFlavor.SDCPP ? (r.modes.length ? r.modes.map((m) => (m === 'vid_gen' ? 'video' : 'image')).join(' · ') : 'image') : undefined) : enumLabel(RouteState, r.state) })));
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
    const stops = session.stop
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    return { temperature: num(session.temperature), topP: num(session.topP), topK: int(session.topK), maxTokens: int(session.maxTokens), seed: int(session.seed), stop: stops.length ? stops : undefined };
  });
  const lastAnswer = $derived([...session.turns].reverse().find((t) => t.role === 'assistant'));
  const shownBody = $derived(lastSent ? elide(lastSent.body) : '');
  const inspectorTabs = $derived([
    { id: 'settings', label: 'Settings' },
    { id: 'request', label: 'Request' },
    { id: 'trace', label: 'Trace' },
    { id: 'log', label: 'Log' }
  ]);

  const modes = $derived(caps?.supported_modes ?? chosen?.modes ?? []);
  const canImage = $derived(modes.includes('img_gen'));
  const canVideo = $derived(modes.includes('vid_gen'));
  $effect(() => {
    if (mode === 'video' && !canVideo && canImage) mode = 'image';
    if (mode === 'image' && !canImage && canVideo) mode = 'video';
  });
  const modeItems = $derived([
    { id: 'image', label: 'Image', unmet: canImage ? '' : `${chosenName} makes no images` },
    { id: 'video', label: 'Video', unmet: canVideo ? '' : `${chosenName} makes no video` }
  ]);
  // Show this model's history oldest first. Track running videos across models.
  const shown = $derived(history.filter((g) => g.model === chosenName).slice().reverse());
  const running = $derived(history.filter((g) => g.video && (g.video.status === 'queued' || g.video.status === 'in_progress')));
  const busyHere = $derived(busy || (media && running.some((g) => g.model === chosenName)));
  const init = $derived(media ? (pending[0] ?? null) : null);
  const last = $derived(media && mode === 'video' ? (pending[1] ?? null) : null);
  const canSend = $derived.by(() => {
    if (!chosen || !chosenReady || attaching > 0) return false;
    if (media) return draft.trim().length > 0 && !busy && (mode === 'image' ? canImage : canVideo);
    return (draft.trim().length > 0 || pending.length > 0) && !busy;
  });
  const applied = $derived.by(() => {
    const d = caps?.defaults_by_mode?.[modeKey(mode)];
    const sp = d?.sample_params;
    const pick = (mine: string, theirs: string) => (mine.trim() ? mine.trim() : theirs);
    const out = [`${pick(form.width, figure(d?.width))}×${pick(form.height, figure(d?.height))}`];
    if (mode === 'video') out.push(`${pick(form.frames, figure(d?.video_frames))} frames`, `${pick(form.fps, figure(d?.fps))} fps`);
    const steps = pick(form.steps, figure(sp?.sample_steps));
    if (steps) out.push(`${steps} steps`);
    const cfg = pick(form.cfg, figure(sp?.guidance.txt_cfg));
    if (cfg) out.push(`cfg ${cfg}`);
    const sampler = pick(form.sampler, namedChoice(sp?.sample_method));
    if (sampler) out.push(sampler);
    if (form.seed.trim()) out.push(`seed ${form.seed.trim()}`);
    if (mode === 'image' && (int(form.n) ?? d?.batch_count ?? 1) > 1) out.push(`${int(form.n) ?? d?.batch_count} images`);
    return out.filter((s) => !s.startsWith('×') && !s.endsWith('×'));
  });

  // Use separate storage prefixes for chat sessions and media settings.
  const sessionPrefix = 'nebu.chat.';
  const sessionKey = (name: string) => sessionPrefix + name;
  const formKey = (name: string) => `nebu.generate.${name}`;
  let loaded = '';
  let loadedForm = '';
  $effect(() => {
    const name = chosenName;
    const isMedia = media;
    untrack(() => {
      if (!name) return;
      if (!isMedia && name !== loaded) {
        loaded = name;
        try {
          const raw = readLocal(sessionKey(name));
          session = raw ? { ...blank(), ...(JSON.parse(raw) as Partial<Session>) } : blank();
        } catch {
          session = blank();
        }
        shownTrace = lastAnswer?.trace ?? '';
      }
      if (isMedia && name !== loadedForm) {
        loadedForm = name;
        try {
          const raw = readLocal(formKey(name));
          const saved = raw ? (JSON.parse(raw) as Partial<MediaForm> & { mode?: Mode }) : null;
          form = { ...blankForm(), ...(saved ?? {}) };
          if (saved?.mode) mode = saved.mode;
        } catch {
          form = blankForm();
        }
        caps = null;
        capsError = '';
        shownTrace = shown[shown.length - 1]?.trace ?? '';
      }
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
  let saveFormTimer: ReturnType<typeof setTimeout> | null = null;
  $effect(() => {
    const snapshot = JSON.stringify({ ...form, mode });
    const name = loadedForm;
    if (!name) return;
    if (saveFormTimer) clearTimeout(saveFormTimer);
    saveFormTimer = setTimeout(() => writeLocal(formKey(name), snapshot), 300);
  });
  let historyLoaded = false;
  $effect(() => {
    const snapshot = history;
    if (historyLoaded) writeHistory(snapshot);
  });

  onMount(() => {
    model = page.url.searchParams.get('model') ?? '';
    history = readHistory();
    historyLoaded = true;
  });
  onDestroy(() => {
    for (const u of urls.values()) URL.revokeObjectURL(u);
  });
  $effect(() => {
    const name = chosenName;
    if (!name || page.url.searchParams.get('model') === name) return;
    const url = new URL(page.url);
    url.searchParams.set('model', name);
    replaceState(url, {});
  });

  $effect(() => {
    const name = chosenName;
    const isReady = chosenReady;
    const isMedia = media;
    const base = gatewayBase;
    const k = key;
    if (!name || !isReady || !isMedia) return;
    const c = new AbortController();
    capabilities(base, name, k, c.signal)
      .then((got) => {
        caps = got;
        capsError = '';
        untrack(() => applyShape(got));
      })
      .catch((err) => {
        if (!c.signal.aborted) capsError = err instanceof Error ? err.message : String(err);
      });
    return () => c.abort();
  });

  // Apply model defaults only while the form is untouched.
  function applyShape(got: Capabilities) {
    if (readLocal(formKey(chosenName))) return;
    const d = got.defaults_by_mode?.[modeKey(mode)];
    if (!d) return;
    if (d.width > 0 && d.height > 0) {
      form.width = String(d.width);
      form.height = String(d.height);
    }
  }

  // Cache object URLs to avoid rereading stored files.
  $effect(() => {
    const ids = [...session.turns.flatMap((t) => [...(t.images ?? []).map((a) => a.id), ...(t.media ?? []).flatMap((m) => (m.id ? [m.id] : []))]), ...pending.map((a) => a.id), ...shown.flatMap((g) => g.files.map((f) => f.id))];
    for (const id of ids) {
      if (urls.has(id) || missing.has(id) || loadingIds.has(id)) continue;
      loadingIds.add(id);
      getImage(id)
        .then((blob) => {
          if (blob) urls.set(id, URL.createObjectURL(blob));
          else missing.add(id);
        })
        .catch((err) => {
          missing.add(id);
          fail(err, 'Could not load a file');
        })
        .finally(() => loadingIds.delete(id));
    }
  });

  async function scroll() {
    await tick();
    log?.scrollTo({ top: log.scrollHeight });
  }

  function stop() {
    controller?.abort();
  }

  function forget(ids: string[]) {
    for (const id of ids) {
      const u = urls.get(id);
      if (u) URL.revokeObjectURL(u);
      urls.delete(id);
      encoded.delete(id);
      missing.delete(id);
    }
    deleteImages(ids).catch((err) => fail(err, 'Could not delete a file'));
  }

  function imageIds(turns: Turn[]): string[] {
    return turns.flatMap((t) => [...(t.images ?? []).map((a) => a.id), ...(t.media ?? []).flatMap((m) => (m.id ? [m.id] : []))]);
  }

  function clear() {
    stop();
    if (media) {
      for (const g of shown) remove(g);
    } else {
      forget(imageIds(session.turns));
      session.turns = [];
    }
    lastSent = null;
    shownTrace = '';
    box?.focus();
  }

  function removeTurn(i: number) {
    forget(imageIds([session.turns[i]]));
    session.turns = session.turns.filter((_, j) => j !== i);
  }

  // Exclude unanswered turns from history.
  function conversation(upTo = session.turns.length): Outgoing[] {
    const out: Outgoing[] = [];
    const past = session.turns.slice(0, upTo);
    for (let i = 0; i < past.length; i++) {
      const t = past[i];
      if (t.role !== 'user') continue;
      const answer = past[i + 1];
      if (answer?.role === 'assistant' && (answer.text || answer.toolCalls?.length) && !answer.error) {
        out.push({ role: 'user', content: t.text, attachments: t.images });
        out.push({ role: 'assistant', content: answer.text, toolCalls: answer.toolCalls?.length ? answer.toolCalls : undefined });
      } else if (i === past.length - 1) {
        out.push({ role: 'user', content: t.text, attachments: t.images });
      }
    }
    return out;
  }

  async function encode(a: Attachment): Promise<{ mediaType: string; data: string }> {
    let data = encoded.get(a.id);
    if (data === undefined) {
      const blob = await getImage(a.id);
      if (!blob) throw new Error(`${a.name} is no longer stored in this browser. Remove the turn it is in to continue.`);
      data = await toBase64(blob);
      encoded.set(a.id, data);
    }
    return { mediaType: a.mediaType, data };
  }

  async function wire(msgs: Outgoing[]): Promise<ChatMessage[]> {
    return Promise.all(msgs.map(async ({ attachments, ...m }) => (attachments?.length ? { ...m, images: await Promise.all(attachments.map(encode)) } : m)));
  }

  function explain(err: unknown): string {
    const text = err instanceof Error ? err.message : String(err);
    if (err instanceof TypeError && own) {
      return `${text}. Could not reach ${gatewayBase}. Open ${gatewayBase}/health once to accept its certificate, or add ${window.location.origin} to gateway.cors_origins.`;
    }
    return text;
  }

  function keep(turn: Turn, url: string) {
    const key = newId();
    turn.media = [...(turn.media ?? []), url.startsWith('data:') ? { key } : { key, url }];
    if (!url.startsWith('data:')) return;
    const set = (patch: Partial<Media>) => {
      turn.media = (turn.media ?? []).map((m) => (m.key === key ? { ...m, ...patch } : m));
    };
    fetchImage(url)
      .then((blob) => storeImage(blob, 'Image from ' + chosenName))
      .then((a) => {
        urls.set(a.id, url);
        set({ id: a.id });
      })
      .catch((err) => set({ error: err instanceof Error ? err.message : String(err) }));
  }

  async function ask(userIndex: number) {
    if (busy || !chosen || !chosenReady) return;
    forget(imageIds(session.turns.slice(userIndex + 1)));
    session.turns = [...session.turns.slice(0, userIndex + 1), { role: 'assistant', text: '', dialect: session.dialect, startedAt: Date.now() }];
    const turn = session.turns[session.turns.length - 1];
    busy = true;
    controller = new AbortController();
    const calls = new Map<number, ToolCall>();
    scroll();
    try {
      const messages = await wire(conversation(userIndex + 1));
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
            case 'image':
              if (!turn.firstTokenAt) turn.firstTokenAt = Date.now();
              keep(turn, ev.image!.url);
              scroll();
              break;
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

  const num = (s: string) => (s.trim() === '' ? undefined : parseFloat(s));
  const int = (s: string) => (s.trim() === '' ? undefined : parseInt(s, 10));

  async function dataUrl(a: Attachment): Promise<string> {
    const { mediaType, data } = await encode(a);
    return `data:${mediaType};base64,${data}`;
  }

  // Choose the random seed here so the generation can be reproduced.
  async function mediaRequest(prompt: string, start: Attachment | null, end: Attachment | null): Promise<MediaRequest> {
    const seed = form.seed.trim() === '' ? Math.floor(Math.random() * 2147483647) : parseInt(form.seed, 10);
    const req: MediaRequest = {
      model: chosen!.name,
      prompt,
      negative_prompt: form.negative.trim() || undefined,
      width: int(form.width),
      height: int(form.height),
      steps: int(form.steps),
      cfg_scale: num(form.cfg),
      seed,
      sampler: form.sampler || undefined,
      scheduler: form.scheduler || undefined,
      guidance: num(form.guidance),
      flow_shift: num(form.flowShift),
      clip_skip: int(form.clipSkip),
      vae_tiling: form.vaeTiling || undefined,
      output_format: form.format || undefined,
      lora: parseLoras(form.loras)
    };
    if (start) {
      req.init_image = await dataUrl(start);
      req.strength = num(form.strength);
    }
    if (mode === 'image') {
      req.n = int(form.n);
    } else {
      req.frames = int(form.frames);
      req.fps = int(form.fps);
      req.temporal_tiling = form.temporalTiling || undefined;
      if (end) req.end_image = await dataUrl(end);
      if (form.highSteps.trim() || form.highCfg.trim()) req.high_noise = { steps: int(form.highSteps), cfg_scale: num(form.highCfg) };
    }
    return req;
  }

  function mimeOf(format: string, video: boolean): string {
    switch (format) {
      case 'jpeg':
        return 'image/jpeg';
      case 'webp':
        return 'image/webp';
      case 'avi':
        return 'video/x-msvideo';
      case 'webm':
        return 'video/webm';
      case 'mp4':
        return 'video/mp4';
    }
    return video ? 'video/webm' : 'image/png';
  }

  function update(id: string, patch: Partial<Generation>) {
    history = history.map((g) => (g.id === id ? { ...g, ...patch } : g));
  }

  async function generate(prompt: string, start: Attachment | null, end: Attachment | null, kind: Mode = mode) {
    if (!chosen || !chosenReady) return;
    let req: MediaRequest;
    try {
      req = await mediaRequest(prompt, start, end);
    } catch (err) {
      fail(err, 'Could not read the attached image');
      return;
    }
    busy = true;
    controller = new AbortController();
    const startedAt = Date.now();
    const sp = caps?.defaults_by_mode?.[modeKey(kind)]?.sample_params;
    const applied = { steps: sp?.sample_steps, cfg: sp?.guidance.txt_cfg, sampler: namedChoice(sp?.sample_method) || undefined, scheduler: namedChoice(sp?.scheduler) || undefined };
    const entry: Generation = { id: newId(), kind, model: chosen.name, createdAt: startedAt, request: req, files: [], format: '', mime: '', inputs: [start, end].filter((a): a is Attachment => !!a), applied };
    history = [entry, ...history];
    scroll();
    try {
      if (kind === 'image') {
        const { images, format } = await generateImages(gatewayBase, entry.request, key, controller.signal, (sent) => {
          lastSent = sent;
          shownTrace = sent.trace;
          update(entry.id, { trace: sent.trace });
        });
        const mime = mimeOf(format, false);
        const files: Attachment[] = [];
        for (const [i, b64] of images.entries()) {
          const blob = await fetchImage(`data:${mime};base64,${b64}`);
          files.push(await storeImage(blob, `${entry.model} ${i + 1}.${format}`));
        }
        update(entry.id, { files, format, mime, elapsedMs: Date.now() - startedAt });
      } else {
        const video = await createVideo(gatewayBase, entry.request, key, controller.signal, (sent) => {
          lastSent = sent;
          shownTrace = sent.trace;
          update(entry.id, { trace: sent.trace });
        });
        update(entry.id, { video: { id: video.id, status: video.status, frames: video.frames, fps: video.fps }, format: video.output_format, mime: video.mime_type });
      }
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') {
        update(entry.id, { error: 'cancelled' });
      } else {
        update(entry.id, { error: explain(err) });
        fail(err, 'Generation failed');
      }
    } finally {
      busy = false;
      controller = null;
      scroll();
      box?.focus();
    }
  }

  // Poll running videos and save completed files.
  $effect(() => {
    const ids = running.map((g) => g.id).join(',');
    if (!ids) return;
    const timer = setInterval(() => untrack(pollVideos), 1500);
    untrack(pollVideos);
    return () => clearInterval(timer);
  });

  let polling = false;
  async function pollVideos() {
    if (polling) return;
    polling = true;
    try {
      for (const g of running) {
        if (!g.video) continue;
        let v;
        try {
          v = await getVideo(gatewayBase, g.video.id, key);
        } catch (err) {
          const text = err instanceof Error ? err.message : String(err);
          if (text.startsWith('404')) update(g.id, { video: { ...g.video, status: 'failed' }, error: 'the gateway no longer has this video' });
          continue;
        }
        if (v.status === 'completed') {
          try {
            const blob = await fetchVideo(gatewayBase, v.id, key);
            const file = await storeBlob(blob, `${g.model}.${v.output_format}`);
            update(g.id, { video: { id: v.id, status: 'completed', frames: v.frames, fps: v.fps }, files: [file], format: v.output_format, mime: v.mime_type || blob.type, elapsedMs: (v.completed_at ?? Date.now() / 1000) * 1000 - g.createdAt });
          } catch (err) {
            update(g.id, { video: { ...g.video, status: 'failed' }, error: explain(err) });
          }
        } else if (v.status === 'failed') {
          update(g.id, { video: { ...g.video, status: 'failed' }, error: v.error?.message ?? 'the video failed' });
        } else if (v.status !== g.video.status) {
          update(g.id, { video: { ...g.video, status: v.status } });
        }
      }
    } finally {
      polling = false;
    }
  }

  async function cancel(g: Generation) {
    if (!g.video) return;
    try {
      await deleteVideo(gatewayBase, g.video.id, key);
      update(g.id, { video: { ...g.video, status: 'failed' }, error: 'cancelled' });
    } catch (err) {
      fail(err, 'Cancel failed');
    }
  }

  function remove(g: Generation) {
    if (g.video && (g.video.status === 'queued' || g.video.status === 'in_progress')) cancel(g);
    forget([...g.files.map((f) => f.id), ...(g.inputs ?? []).map((a) => a.id)]);
    history = history.filter((x) => x.id !== g.id);
  }

  function reuse(g: Generation) {
    const r = g.request;
    mode = g.kind;
    draft = r.prompt;
    form = {
      ...form,
      negative: r.negative_prompt ?? '',
      width: String(r.width ?? form.width),
      height: String(r.height ?? form.height),
      steps: r.steps?.toString() ?? '',
      cfg: r.cfg_scale?.toString() ?? '',
      seed: r.seed?.toString() ?? '',
      sampler: r.sampler ?? '',
      scheduler: r.scheduler ?? '',
      n: r.n?.toString() ?? '',
      frames: r.frames?.toString() ?? '',
      fps: r.fps?.toString() ?? '',
      strength: r.strength?.toString() ?? '',
      guidance: r.guidance?.toString() ?? '',
      flowShift: r.flow_shift?.toString() ?? '',
      clipSkip: r.clip_skip?.toString() ?? '',
      vaeTiling: !!r.vae_tiling,
      temporalTiling: !!r.temporal_tiling,
      highSteps: r.high_noise?.steps?.toString() ?? '',
      highCfg: r.high_noise?.cfg_scale?.toString() ?? '',
      format: r.output_format ?? '',
      loras: lorasText(r.lora)
    };
    pane = 'settings';
    inspector = true;
    ok('Settings restored', 'The prompt, size, seed, and sampler are back as they were');
    tick().then(() => {
      grow();
      box?.focus();
    });
  }

  function download(f: Attachment) {
    const url = urls.get(f.id);
    if (!url) return;
    const a = document.createElement('a');
    a.href = url;
    a.download = f.name.replace(/[^\w.-]+/g, '_');
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  }

  function summary(g: Generation): string[] {
    const r = g.request;
    const out = [`${r.width}×${r.height}`];
    if (g.kind === 'video') out.push(`${g.video?.frames ?? r.frames ?? '?'} frames · ${g.video?.fps ?? r.fps ?? '?'} fps`);
    const steps = r.steps ?? g.applied?.steps;
    if (steps) out.push(`${steps} steps`);
    const cfg = r.cfg_scale ?? g.applied?.cfg;
    if (cfg !== undefined) out.push(`cfg ${cfg}`);
    const sampler = r.sampler ?? g.applied?.sampler;
    if (sampler) out.push(sampler);
    if (r.lora?.length) out.push(`${r.lora.length} LoRA${r.lora.length > 1 ? 's' : ''}`);
    if (r.seed !== undefined) out.push(`seed ${r.seed}`);
    if (g.elapsedMs) out.push(ms(g.elapsedMs));
    return out;
  }

  function submit() {
    if (!canSend || !chosen) return;
    const text = draft.trim();
    const images = pending;
    pending = [];
    draft = '';
    if (box) box.style.height = '';
    if (media) {
      generate(text, images[0] ?? null, mode === 'video' ? (images[1] ?? null) : null);
      forget(images.slice(mode === 'video' ? 2 : 1).map((a) => a.id));
      return;
    }
    session.turns = [...session.turns, { role: 'user', text, images: images.length ? images : undefined }];
    ask(session.turns.length - 1);
  }

  function regenerate() {
    if (media) {
      const g = shown[shown.length - 1];
      if (!g || busy) return;
      generate(g.request.prompt, g.inputs?.[0] ?? null, g.inputs?.[1] ?? null, g.kind);
      return;
    }
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

  function grow() {
    if (!box) return;
    box.style.height = '';
    box.style.height = Math.min(box.scrollHeight, 200) + 'px';
  }

  const attachLimit = $derived(!media ? Infinity : mode === 'video' ? 2 : 1);
  const attachNote = $derived.by(() => {
    if (blind) return blindNote;
    if (!media) return 'Attach an image, or paste or drop one';
    if (mode === 'video') return pending.length ? 'Attach the last frame' : 'Attach a start image, and a last frame after it';
    return 'Attach an image to start from';
  });

  async function attach(files: File[]) {
    if (!files.length) return;
    if (blind) {
      fail(new Error(blindNote), 'Cannot attach');
      return;
    }
    for (const f of files) {
      const name = f.name || 'pasted image';
      attaching++;
      try {
        const { attachment, blob } = await prepareImage(f, name);
        await putImage(attachment.id, blob);
        urls.set(attachment.id, URL.createObjectURL(blob));
        encoded.set(attachment.id, await toBase64(blob));
        if (pending.length >= attachLimit) {
          const dropped = pending[pending.length - 1];
          pending = [...pending.slice(0, -1), attachment];
          forget([dropped.id]);
        } else {
          pending = [...pending, attachment];
        }
      } catch (err) {
        fail(err, `Could not attach ${name}`);
      } finally {
        attaching--;
      }
    }
    box?.focus();
  }

  function unattach(a: Attachment) {
    pending = pending.filter((p) => p.id !== a.id);
    forget([a.id]);
  }

  function onPick(e: Event) {
    const el = e.currentTarget as HTMLInputElement;
    attach([...(el.files ?? [])]);
    el.value = '';
  }

  function onPaste(e: ClipboardEvent) {
    const files = [...(e.clipboardData?.files ?? [])].filter((f) => f.type.startsWith('image/'));
    if (!files.length) return;
    e.preventDefault();
    attach(files);
  }

  // Track drag depth because entering a child fires dragleave on its parent.
  function hasFiles(e: DragEvent): boolean {
    return !!e.dataTransfer && [...e.dataTransfer.types].includes('Files');
  }
  function onDragEnter(e: DragEvent) {
    if (!hasFiles(e)) return;
    e.preventDefault();
    dragDepth++;
  }
  function onDragOver(e: DragEvent) {
    if (!hasFiles(e)) return;
    e.preventDefault();
    e.dataTransfer!.dropEffect = blind ? 'none' : 'copy';
  }
  function onDragLeave(e: DragEvent) {
    if (!hasFiles(e)) return;
    dragDepth = Math.max(0, dragDepth - 1);
  }
  function onDrop(e: DragEvent) {
    if (!hasFiles(e)) return;
    e.preventDefault();
    dragDepth = 0;
    attach([...(e.dataTransfer?.files ?? [])]);
  }

  function show(url: string | undefined, alt: string) {
    if (!url) return;
    lightbox = url;
    lightboxAlt = alt;
  }

  // Debounce token counts. Read primitive route fields to avoid recounting on traffic updates.
  $effect(() => {
    const text = draft;
    const sys = session.system;
    const turnsSnapshot = session.turns.length;
    const attached = pending.map((a) => a.id).join(',');
    const name = chosenName;
    const isReady = chosenReady;
    const isMedia = media;
    void turnsSnapshot;
    void attached;
    countController?.abort();
    if (!name || !isReady || isMedia) {
      promptTokens = null;
      return;
    }
    const c = new AbortController();
    countController = c;
    const timer = setTimeout(async () => {
      try {
        const attachments = untrack(() => pending);
        const last: Outgoing[] = text.trim() || attachments.length ? [{ role: 'user', content: text.trim(), attachments }] : [];
        const messages = [...conversation(), ...last];
        if (messages.length === 0 && !sys.trim()) {
          promptTokens = null;
          return;
        }
        promptTokens = await countTokens(gatewayBase, name, sys.trim(), await wire(messages), key, c.signal);
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
  function stopNote(s: string | undefined): string {
    const w = stopWord(s);
    if (!w || w === 'stop') return '';
    if (w === 'length') return 'cut off at max tokens';
    if (w === 'tool') return 'stopped to call a tool';
    return `stopped: ${w}`;
  }

  // Replace image bytes with their size in the inspector.
  function elide(body: string): string {
    return body.replace(/[A-Za-z0-9+/]{256,}={0,2}(?=")/g, (m) => `…${bytes((m.length * 3) / 4, 0)} of image data…`);
  }

  const emptyTurns = $derived(media ? shown.length === 0 : session.turns.length === 0);
  const hasTurns = $derived(!emptyTurns);
</script>

<svelte:head><title>Chat · nebu</title></svelte:head>

{#snippet thumb(a: Attachment, size: string)}
  {#if missing.has(a.id)}
    <div class="flex {size} flex-col items-center justify-center gap-1 rounded-md border border-dashed border-line text-xs text-fg-faint" title="{a.name} is no longer stored in this browser"><ImageOff size={16} />image missing</div>
  {:else if urls.has(a.id)}
    <button type="button" class="block overflow-hidden rounded-md border border-line" onclick={() => show(urls.get(a.id), a.name)} aria-label="Open {a.name}">
      <img src={urls.get(a.id)} alt={a.name} width={a.width} height={a.height} class="block h-auto max-h-64 w-auto max-w-full object-contain" />
    </button>
  {:else}
    <div class="flex {size} items-center justify-center rounded-md border border-line"><Spinner size={14} class="text-fg-faint" /></div>
  {/if}
{/snippet}

{#if loading}
  <div class="h-[calc(100vh-6rem)]" aria-busy="true"></div>
{:else if routes.length === 0}
  <Empty icon={MessageSquare} title="Run a model to chat or generate images and video.">
    {#if live.models.size}<Button variant="primary" href="/store">Library</Button>{:else}<Button variant="primary" href="/catalog">Browse catalog</Button>{/if}
  </Empty>
{:else}
  <div class="flex h-[calc(100vh-4.5rem)] min-h-[32rem] gap-4 lg:h-[calc(100vh-2.5rem)] {dragging ? 'select-none' : ''}">
    <section class="card relative flex min-w-0 flex-1 flex-col" aria-label="Conversation" ondragenter={onDragEnter} ondragover={onDragOver} ondragleave={onDragLeave} ondrop={onDrop}>
      {#if dropping}
        <div class="pointer-events-none absolute inset-0 z-20 flex items-center justify-center rounded-lg border-2 border-dashed bg-bg/80 {blind ? 'border-warn' : 'border-accent'}">
          <div class="flex items-center gap-2 text-sm {blind ? 'text-warn' : 'text-fg'}">
            {#if blind}<ImageOff size={18} />{blindNote}{:else}<ImagePlus size={18} />{media ? (mode === 'video' && pending.length ? 'Drop the last frame' : 'Drop an image to start from') : 'Drop to attach'}{/if}
          </div>
        </div>
      {/if}
      <header class="flex items-center gap-3 border-b border-line px-4 py-2.5">
        <Select class="w-80 max-w-[40%] shrink-0" mono bind:value={model} label="Model" items={modelItems} />
        {#if chosen}
          <State values={RouteState} value={chosen.state} class="shrink-0" />
          {#if media && canImage && canVideo}
            <Segmented size="sm" bind:value={mode} tabs={modeItems} class="shrink-0" />
          {/if}
          {#if instance}
            <span class="hidden min-w-0 flex-1 items-center gap-x-3 overflow-hidden text-xs whitespace-nowrap text-fg-muted md:flex">
              <span class="kv"><span>runtime</span><span>{runtimeName(instance.runtimeId)}{install?.version ? ` ${install.version}` : ''}</span></span>
              {#if instance.params.n_ctx}<span class="kv"><span>ctx</span><span>{instance.params.n_ctx}</span></span>{/if}
              {#if media}<span class="kv"><span>makes</span><span>{canImage && canVideo ? 'images, video' : canVideo ? 'video' : 'images'}</span></span>{:else if vision}<span class="kv" title="The model has a vision encoder"><span>sees</span><span>images</span></span>{/if}
              {#if slot}<a class="kv link" href="/slots/{slot.id}"><span>slot</span><span>{slot.name}</span></a>{:else}<a class="link" href="/instances/{instance.id}">Open instance</a>{/if}
              {#if instanceLive(instance)}<span class="kv"><span>up</span><span>{duration(instance.readyAt ?? instance.createdAt, undefined, clock.now)}</span></span>{/if}
            </span>
          {/if}
        {/if}
        <span class="ml-auto flex shrink-0 items-center gap-1">
          <IconButton icon={RotateCcw} label={media ? 'Make the last prompt again' : 'Regenerate the last answer'} onclick={regenerate} disabled={busy || !hasTurns} />
          <IconButton icon={Trash2} label="Clear the conversation" onclick={clear} disabled={!hasTurns} />
          <IconButton icon={inspector ? PanelRightClose : PanelRightOpen} label={inspector ? 'Hide the side panel' : 'Show the side panel'} onclick={() => (inspector = !inspector)} />
        </span>
      </header>

      {#if chosen && !chosenReady}
        <div class="note note-warn m-4 mb-0">{chosen.name} is {enumLabel(RouteState, chosen.state)}. Requests will wait until it is ready.</div>
      {/if}
      {#if media && capsError}
        <div class="note note-bad m-4 mb-0">Could not load {chosenName} capabilities: {capsError}</div>
      {/if}

      <div bind:this={log} class="min-h-0 flex-1 overflow-y-auto px-4">
        <div class="mx-auto flex max-w-3xl flex-col gap-5 py-4">
          {#if emptyTurns}
            <div class="flex flex-col items-center justify-center gap-3 py-20 text-center">
              <Logo size={36} class="text-fg-faint" />
              <div class="font-mono text-sm text-fg-muted">{chosen?.name}</div>
              {#if media}
                <div class="text-xs text-fg-faint">{canVideo && canImage ? 'Describe an image or a video to make. Paste or drop an image to start from one.' : canVideo ? 'Describe a video to make. Paste or drop an image to start from it.' : 'Describe an image to make. Paste or drop an image to start from it.'}</div>
              {:else if vision}
                <div class="text-xs text-fg-faint">Paste, drop, or attach an image to ask about it</div>
              {/if}
            </div>
          {/if}
          {#if media}
            {#each shown as g (g.id)}
              <div class="group flex items-start justify-end gap-2">
                <button type="button" class="mt-2 rounded-md p-1 text-fg-faint opacity-0 transition-opacity group-hover:opacity-100 hover:bg-raised hover:text-fg" aria-label="Remove" onclick={() => remove(g)}><X size={13} /></button>
                <div class="max-w-[85%] rounded-xl rounded-br-sm bg-raised px-4 py-2.5 text-sm text-fg">
                  {#if g.inputs?.length}
                    <div class="mb-2 flex flex-wrap justify-end gap-2">
                      {#each g.inputs as a (a.id)}{@render thumb(a, 'h-24 w-32')}{/each}
                    </div>
                  {/if}
                  <div class="whitespace-pre-wrap">{g.request.prompt}</div>
                  {#if g.request.negative_prompt}<div class="mt-1 text-xs text-fg-faint">not: {g.request.negative_prompt}</div>{/if}
                </div>
              </div>
              <div class="flex gap-3">
                <Logo size={18} class="mt-1 shrink-0 text-fg-faint" />
                <div class="min-w-0 flex-1">
                  {#if g.error}
                    <div class="note note-bad">{g.error}</div>
                  {:else if g.video && g.video.status !== 'completed'}
                    <div class="flex items-center gap-3 rounded-md border border-line bg-sunken px-4 py-4 text-sm text-fg-muted">
                      <Spinner size={16} class="text-accent" />
                      <span>{g.video.status === 'queued' ? 'Queued' : 'Making'} {g.request.frames ?? ''} frames at {g.request.width}×{g.request.height}</span>
                      <span class="ml-auto"><Button size="sm" variant="danger" icon={Square} onclick={() => cancel(g)}>Cancel</Button></span>
                    </div>
                  {:else if g.files.length === 0}
                    <div class="flex items-center gap-3 rounded-md border border-line bg-sunken px-4 py-4 text-sm text-fg-muted"><Spinner size={16} class="text-accent" /> Making {g.kind === 'video' ? 'a video' : (g.request.n ?? 1) > 1 ? `${g.request.n} images` : 'an image'} at {g.request.width}×{g.request.height}…</div>
                  {:else}
                    <div class="flex flex-wrap gap-3">
                      {#each g.files as f (f.id)}
                        {@const url = urls.get(f.id)}
                        <div class="group/file relative max-w-full">
                          {#if missing.has(f.id)}
                            <div class="flex h-40 w-56 flex-col items-center justify-center gap-1 rounded-md border border-dashed border-line text-xs text-fg-faint"><ImageOff size={16} />file missing</div>
                          {:else if !url}
                            <div class="flex h-40 w-56 items-center justify-center rounded-md border border-line"><Spinner size={14} class="text-fg-faint" /></div>
                          {:else if g.mime.startsWith('video/') && g.mime !== 'video/x-msvideo'}
                            <!-- svelte-ignore a11y_media_has_caption -->
                            <video src={url} controls loop muted playsinline class="block max-h-[28rem] max-w-full rounded-md border border-line bg-sunken" style="width: {g.request.width}px"></video>
                          {:else if g.mime === 'video/x-msvideo'}
                            <div class="flex h-40 w-56 flex-col items-center justify-center gap-2 rounded-md border border-line text-xs text-fg-muted"><Film size={18} /><span>AVI video · {bytes(f.bytes)}</span><Button size="sm" icon={Download} onclick={() => download(f)}>Download</Button></div>
                          {:else}
                            <button type="button" class="block overflow-hidden rounded-md border border-line" onclick={() => show(url, g.request.prompt)} aria-label="Open image">
                              <img src={url} alt={g.request.prompt} class="block h-auto max-h-[28rem] w-auto max-w-full object-contain" loading="lazy" />
                            </button>
                          {/if}
                          {#if url && !missing.has(f.id)}
                            <button type="button" class="absolute top-2 right-2 rounded-md border border-line bg-raised/90 p-1.5 text-fg-muted opacity-0 transition-opacity group-hover/file:opacity-100 hover:text-fg" aria-label="Download" onclick={() => download(f)}><Download size={13} /></button>
                          {/if}
                        </div>
                      {/each}
                    </div>
                  {/if}
                  <div class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs tabular-nums text-fg-faint">
                    {#each summary(g) as s (s)}<span>{s}</span>{/each}
                    <span>{new Date(g.createdAt).toLocaleString()}</span>
                    {#if g.trace}
                      <button type="button" class="inline-flex items-center gap-1 text-fg-muted hover:text-fg" onclick={() => { shownTrace = g.trace!; pane = 'trace'; inspector = true; }}><FileJson size={12} /> trace</button>
                    {/if}
                    <button type="button" class="inline-flex items-center gap-1 text-fg-muted hover:text-fg" onclick={() => reuse(g)}><Settings2 size={12} /> reuse settings</button>
                    <Copy text={g.request.prompt} size={12} class="h-6 w-6" label="Copy the prompt" />
                  </div>
                </div>
              </div>
            {/each}
          {:else}
            {#each session.turns as t, i (i)}
              {#if t.role === 'user'}
                <div class="group flex items-start justify-end gap-2">
                  <button type="button" class="mt-2 rounded-md p-1 text-fg-faint opacity-0 transition-opacity group-hover:opacity-100 hover:bg-raised hover:text-fg" aria-label="Remove" onclick={() => removeTurn(i)}><X size={13} /></button>
                  <div class="max-w-[85%] rounded-xl rounded-br-sm bg-raised px-4 py-2.5 text-sm text-fg">
                    {#if t.images?.length}
                      <div class="flex flex-wrap justify-end gap-2 {t.text ? 'mb-2' : ''}">
                        {#each t.images as a (a.id)}{@render thumb(a, 'h-24 w-32')}{/each}
                      </div>
                    {/if}
                    {#if t.text}<div class="whitespace-pre-wrap">{t.text}</div>{/if}
                  </div>
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
                    {:else if !t.error && !t.toolCalls?.length && !t.media?.length}
                      <span class="text-fg-faint">{t.finishedAt ? 'Empty answer' : '…'}</span>
                    {/if}
                    {#each t.media ?? [] as m, mi (m.key)}
                      {@const src = m.url ?? (m.id ? urls.get(m.id) : undefined)}
                      {#if m.error}
                        <div class="note note-bad mt-2">Could not save image {mi + 1}: {m.error}</div>
                      {:else if m.id && missing.has(m.id)}
                        <div class="mt-2 flex h-24 w-32 flex-col items-center justify-center gap-1 rounded-md border border-dashed border-line text-xs text-fg-faint"><ImageOff size={16} />image missing</div>
                      {:else if src}
                        <button type="button" class="mt-2 block overflow-hidden rounded-md border border-line" onclick={() => show(src, `Image ${mi + 1} from ${t.dialect ?? 'the answer'}`)} aria-label="Open image {mi + 1}">
                          <img {src} alt="Image {mi + 1} of the answer" class="block h-auto max-h-96 w-auto max-w-full object-contain" loading="lazy" />
                        </button>
                      {:else}
                        <div class="mt-2 flex h-24 w-32 items-center justify-center rounded-md border border-line"><Spinner size={14} class="text-fg-faint" /></div>
                      {/if}
                    {/each}
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
          {/if}
        </div>
      </div>

      <div class="border-t border-line p-3">
        <div class="mx-auto flex max-w-3xl flex-col gap-1.5">
          <div class="rounded-xl border bg-sunken transition-colors focus-within:border-accent {dropping ? 'border-dashed border-accent' : 'border-line'}">
            {#if pending.length || attaching}
              <div class="flex flex-wrap gap-2 px-3 pt-3">
                {#each pending as a, ai (a.id)}
                  <div class="relative">
                    <button type="button" class="block overflow-hidden rounded-md border border-line" onclick={() => show(urls.get(a.id), a.name)} aria-label="Open {a.name}" title="{a.name} · {a.width}×{a.height} · {bytes(a.bytes)}">
                      <img src={urls.get(a.id)} alt={a.name} class="block h-16 w-16 object-cover" />
                    </button>
                    {#if media}<span class="absolute bottom-0.5 left-0.5 rounded-sm bg-bg/80 px-1 text-[10px] text-fg-muted">{ai === 0 ? 'start' : 'last frame'}</span>{/if}
                    <button type="button" class="absolute -top-1.5 -right-1.5 rounded-full border border-line bg-raised p-0.5 text-fg-muted transition-colors hover:bg-line-strong hover:text-fg" aria-label="Remove {a.name}" onclick={() => unattach(a)}><X size={11} /></button>
                  </div>
                {/each}
                {#each { length: attaching } as _, ai (ai)}
                  <div class="flex h-16 w-16 items-center justify-center rounded-md border border-line" aria-label="Reading an image"><Spinner size={14} class="text-fg-faint" /></div>
                {/each}
              </div>
            {/if}
            <textarea
              bind:this={box}
              class="block max-h-[200px] w-full resize-none bg-transparent px-3 pt-2.5 pb-1 text-sm leading-6 text-fg placeholder:text-fg-faint focus:outline-none"
              rows="1"
              placeholder={!chosen ? 'Pick a model' : media ? (mode === 'video' ? `Describe a video for ${chosen.name}` : `Describe an image for ${chosen.name}`) : `Message ${chosen.name}`}
              bind:value={draft}
              onkeydown={onKey}
              oninput={grow}
              onpaste={onPaste}
            ></textarea>
            <div class="flex items-center gap-1 px-1.5 pb-1.5">
              <IconButton icon={ImagePlus} label={attachNote} onclick={() => picker?.click()} disabled={blind || busy} />
              <input bind:this={picker} type="file" accept="image/*" multiple={!media} class="hidden" onchange={onPick} />
              <span class="ml-auto hidden items-center gap-1 text-xs text-fg-faint sm:inline-flex"><kbd class="kbd">Enter</kbd> sends, <kbd class="kbd">Shift</kbd>+<kbd class="kbd">Enter</kbd> breaks a line</span>
              {#if busy}
                <Button variant="danger" icon={Square} onclick={stop} aria-label="Stop" />
              {:else}
                <Button variant="primary" icon={ArrowUp} onclick={submit} disabled={!canSend} aria-label="Send" />
              {/if}
            </div>
          </div>
          <div class="flex flex-wrap items-center gap-x-3 px-1 text-xs text-fg-faint">
            {#if media}
              <span class="font-mono">{mode}</span>
              {#each applied as s (s)}<span class="tabular-nums">{s}</span>{/each}
              {#if init}<span>{mode === 'video' ? 'image to video' : 'image to image'}</span>{/if}
              {#if last}<span>with a last frame</span>{/if}
              {#if busyHere && !busy}<span>a video is still being made</span>{/if}
            {:else}
              <span class="font-mono">{session.dialect}{session.stream ? ' · stream' : ''}</span>
              {#if promptTokens !== null}<span title="Counted by the gateway, through the runtime's tokenizer when it has one, each image by its area">{promptTokens} prompt tokens{instance?.params.n_ctx ? ` of ${instance.params.n_ctx}` : ''}</span>{:else if countError}<span class="text-warn" title={countError}>token count unavailable</span>{/if}
            {/if}
          </div>
        </div>
      </div>
    </section>

    {#if inspector}
      <aside class="card relative flex shrink-0 flex-col" style="width: {width}px">
        <div role="separator" aria-orientation="vertical" aria-label="Resize" class="group absolute inset-y-0 -left-1 z-10 w-2 cursor-col-resize" onpointerdown={startDrag}>
          <div class="mx-auto h-full w-px bg-transparent transition-colors group-hover:bg-accent/60 {dragging ? 'bg-accent' : ''}"></div>
        </div>
        <Tabs size="sm" bind:value={pane} tabs={inspectorTabs} class="px-3 pt-1" />
        <div class="@container min-h-0 flex-1 overflow-y-auto p-4">
          {#if pane === 'settings'}
            {#if media}
              <div class="flex flex-col gap-5">
                <MediaSettings bind:form {mode} {caps} hasInit={!!init} />
                {#if status?.auth}
                  <Field label="API key" for="chat-key">
                    <TextInput id="chat-key" mono type="password" bind:value={key} onchange={() => setGatewayKey(key.trim())} />
                  </Field>
                {/if}
                <p class="truncate font-mono text-xs text-fg-faint" title={gatewayBase}>{gatewayBase}</p>
              </div>
            {:else}
              <div class="flex flex-col gap-5">
                <Field label="Wire format">
                  <Segmented bind:value={session.dialect} tabs={[{ id: 'openai', label: 'OpenAI' }, { id: 'anthropic', label: 'Anthropic' }, { id: 'ollama', label: 'Ollama' }]} />
                </Field>
                <div class="flex items-center justify-between gap-4 rounded-md border border-line px-3 py-2.5">
                  <div class="text-[13px] font-medium text-fg">Stream</div>
                  <Switch bind:checked={session.stream} label="Stream" />
                </div>
                <Field label="System prompt" for="chat-system">
                  <TextArea id="chat-system" bind:value={session.system} />
                </Field>
                <div class="grid grid-cols-2 gap-3 @lg:grid-cols-3">
                  <Field label="Temperature" for="chat-temp"><NumberInput id="chat-temp" min={0} max={2} step={0.1} bind:value={session.temperature} /></Field>
                  <Field label="Top P" for="chat-topp"><NumberInput id="chat-topp" min={0} max={1} step={0.05} bind:value={session.topP} /></Field>
                  <Field label="Top K" for="chat-topk"><NumberInput id="chat-topk" integer min={0} bind:value={session.topK} /></Field>
                  <Field label="Max tokens" for="chat-max"><NumberInput id="chat-max" integer min={1} step={64} bind:value={session.maxTokens} /></Field>
                  <Field label="Seed" for="chat-seed"><NumberInput id="chat-seed" integer min={0} bind:value={session.seed} /></Field>
                  <Field label="Stop sequences" for="chat-stop"><TextInput id="chat-stop" mono bind:value={session.stop} empty="###, User:" /></Field>
                </div>
                <Field label="Tools" for="chat-tools" error={toolsProblem || undefined}>
                  <TextArea id="chat-tools" mono height="h-36" bind:value={session.tools} empty={'[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]'} invalid={!!toolsProblem} />
                </Field>
                {#if status?.auth}
                  <Field label="API key" for="chat-key">
                    <TextInput id="chat-key" mono type="password" bind:value={key} onchange={() => setGatewayKey(key.trim())} />
                  </Field>
                {/if}
                <p class="truncate font-mono text-xs text-fg-faint" title={gatewayBase}>{gatewayBase}</p>
              </div>
            {/if}
          {:else if pane === 'request'}
            {#if lastSent}
              <div class="flex flex-col gap-4">
                <div class="grid grid-cols-2 gap-3">
                  <div class="stat"><dt>Path</dt><dd class="font-mono wrap-anywhere">{lastSent.path}</dd></div>
                  <div class="stat"><dt>Status</dt><dd>{lastSent.status}</dd></div>
                </div>
                {#if Object.keys(lastSent.headers ?? {}).length}
                  <div>
                    <div class="caps mb-2 text-fg-faint">Headers</div>
                    <pre class="code whitespace-pre-wrap">{Object.entries(lastSent.headers).map(([k, v]) => `${k}: ${k.toLowerCase() === 'authorization' || k.toLowerCase() === 'x-api-key' ? '••••' : v}`).join('\n')}</pre>
                  </div>
                {/if}
                <div>
                  <div class="mb-2 flex items-center gap-2">
                    <span class="caps text-fg-faint">Body</span>
                    {#if shownBody !== lastSent.body}<span class="text-xs text-fg-faint">image bytes shortened</span><Copy text={lastSent.body} size={12} class="h-6 w-6" label="Copy the exact body" />{/if}
                  </div>
                  <Json text={shownBody} height="max-h-[32rem]" />
                </div>
              </div>
            {:else}
              <p class="text-sm text-fg-faint">No request sent yet</p>
            {/if}
          {:else if pane === 'trace'}
            {#if shownTrace}
              <TraceDetail id={shownTrace} />
            {:else}
              <p class="text-sm text-fg-faint">No trace yet</p>
            {/if}
          {:else if pane === 'log'}
            {#if instance}
              {#key instance.id}<InstanceLog id={instance.id} follow={instanceLive(instance)} height="h-[calc(100vh-14rem)]" />{/key}
            {:else}
              <p class="text-sm text-fg-faint">No instance yet</p>
            {/if}
          {/if}
        </div>
      </aside>
    {/if}
  </div>
  <Lightbox bind:src={lightbox} alt={lightboxAlt} />
{/if}
