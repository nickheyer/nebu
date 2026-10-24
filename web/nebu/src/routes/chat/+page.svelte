<script lang="ts">
  import { onDestroy, onMount, tick, untrack } from 'svelte';
  import { SvelteMap, SvelteSet } from 'svelte/reactivity';
  import { page } from '$app/state';
  import { replaceState } from '$app/navigation';
  import { api, baseUrl, gatewayKey, setGatewayKey, token, message } from '$lib/api';
  import { listenerUrl } from '$lib/gateway';
  import { live, cached, slotByRef, instanceLive, modelKey } from '$lib/state.svelte';
  import { auth } from '$lib/auth.svelte';
  import { readLocal, writeLocal } from '$lib/persist';
  import { byName, enumLabel } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { send, countTokens, toolHeaders, ghostHeaders, type ChatMessage, type Sampling, type ToolCall, type Sent } from '$lib/chatClient';
  import { gatewayTools } from '$lib/tools';
  import { blank, fresh, fileIds, fromConversation, toConversation, loadFile, uploadFile, deleteFiles, previewTitle, type Session, type Turn, type Media } from '$lib/chats';
  import { capabilities, createVideo, deleteVideo, fetchVideo, figure, generateImages, getVideo, lorasText, modeKey, namedChoice, parseLoras, readHistory, writeHistory, type Capabilities, type Generation, type MediaRequest } from '$lib/generate';
  import { prepareImage, storeImage, storeBlob, fetchImage, putImage, getImage, deleteImages, toBase64, newId, type Attachment } from '$lib/images';
  import { RouteState } from '$proto/gateway_pb';
  import { ApiFlavor } from '$proto/runtime_pb';
  import { ArtifactRole, TensorGroupKind } from '$proto/model_pb';
  import type { Conversation } from '$proto/chat_pb';
  import { MessageSquare, MessageSquarePlus, Ghost, ImagePlus, ImageOff } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Lightbox from '$lib/components/ui/Lightbox.svelte';
  import Logo from '$lib/components/Logo.svelte';
  import InstanceLog from '$lib/components/InstanceLog.svelte';
  import TraceDetail from '$lib/components/TraceDetail.svelte';
  import { type MediaForm } from '$lib/components/chat/MediaSettings.svelte';
  import ChatHeader from '$lib/components/chat/ChatHeader.svelte';
  import ChatTurns from '$lib/components/chat/ChatTurns.svelte';
  import MediaHistory from '$lib/components/chat/MediaHistory.svelte';
  import Composer from '$lib/components/chat/Composer.svelte';
  import ChatInspector from '$lib/components/chat/ChatInspector.svelte';
  import ChatList from '$lib/components/chat/ChatList.svelte';
  import ChatSettings from '$lib/components/chat/ChatSettings.svelte';
  import RequestPane from '$lib/components/chat/RequestPane.svelte';

  type Mode = 'image' | 'video';

  type Outgoing = ChatMessage & { attachments?: Attachment[] };

  const blankForm = (): MediaForm => ({ negative: '', width: '1024', height: '1024', steps: '', cfg: '', seed: '', sampler: '', scheduler: '', n: '', frames: '', fps: '', strength: '', guidance: '', flowShift: '', clipSkip: '', vaeTiling: false, temporalTiling: false, highSteps: '', highCfg: '', format: '', loras: '' });

  let model = $state('');
  let session = $state<Session>(blank());
  // The conversation on screen. Its id is chosen here and saved with the daemon
  // on the first answer unless ghost mode is on.
  let convId = $state('');
  let convTitle = $state('');
  let saved = $state(false);
  let saveError = $state('');
  // What the daemon holds, to skip saving a conversation just opened or already saved.
  let savedSnapshot = '';
  let ghost = $state(false);
  let chats = $state<Conversation[]>([]);
  let chatsLoaded = $state(false);
  let chatsError = $state('');
  let opening = $state('');
  let draft = $state('');
  let busy = $state(false);
  // The API token this browser sends to the daemon opens the gateway too
  let key = $state(gatewayKey() || token());
  let inspector = $state(true);
  let pane = $state('settings');
  let shownTrace = $state('');
  let lastSent = $state<Sent | null>(null);
  let promptTokens = $state<number | null>(null);
  let countError = $state('');
  let log: HTMLDivElement | undefined = $state();
  let composer = $state<ReturnType<typeof Composer>>();
  let controller: AbortController | null = null;
  let countController: AbortController | null = null;

  let pending = $state<Attachment[]>([]);
  let attaching = $state(0);
  let dragDepth = $state(0);
  const dropping = $derived(dragDepth > 0);
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
  const inspectorTabs = $derived([
    { id: 'chats', label: 'Chats', count: chats.length || undefined },
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

  // Media settings stay in this browser per model. Conversations live with the daemon.
  const formKey = (name: string) => `nebu.generate.${name}`;
  let loadedForm = '';
  $effect(() => {
    const name = chosenName;
    const isMedia = media;
    untrack(() => {
      if (!name) return;
      if (isMedia && name !== loadedForm) {
        loadedForm = name;
        try {
          const raw = readLocal(formKey(name));
          const stored = raw ? (JSON.parse(raw) as Partial<MediaForm> & { mode?: Mode }) : null;
          form = { ...blankForm(), ...(stored ?? {}) };
          if (stored?.mode) mode = stored.mode;
        } catch {
          form = blankForm();
        }
        caps = null;
        capsError = '';
        shownTrace = shown[shown.length - 1]?.trace ?? '';
      }
    });
  });
  // Save the conversation after each change once an answer is in. Streaming
  // answers save when they finish. Ghost chats and media sessions never save.
  let saveTimer: ReturnType<typeof setTimeout> | null = null;
  $effect(() => {
    const id = convId;
    const isGhost = ghost;
    const isBusy = busy;
    const isMedia = media;
    if (!id || isGhost || isBusy || isMedia) return;
    const snapshot = JSON.stringify(session);
    void snapshot;
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = setTimeout(() => untrack(() => save(id)), 500);
  });

  async function save(id: string) {
    if (id !== convId || ghost || busy) return;
    if (session.turns.length === 0) {
      if (saved) await discardSaved(id);
      return;
    }
    const snapshot = JSON.stringify(session);
    if (snapshot === savedSnapshot) return;
    try {
      const r = await api.chats.putConversation({ conversation: toConversation(id, convTitle, chosenName, session) });
      if (!r.conversation) throw new Error('the daemon returned no conversation');
      const c = r.conversation;
      saveError = '';
      if (id !== convId) return;
      savedSnapshot = snapshot;
      saved = true;
      convTitle = c.title;
      upsertChat({ ...c, turns: [], settings: undefined });
    } catch (err) {
      saveError = message(err);
    }
  }

  function upsertChat(c: Conversation) {
    chats = [c, ...chats.filter((x) => x.id !== c.id)];
  }

  // Removes a saved conversation the user emptied turn by turn.
  async function discardSaved(id: string) {
    try {
      await api.chats.deleteConversation({ id });
      chats = chats.filter((x) => x.id !== id);
      if (id === convId) {
        saved = false;
        convTitle = '';
      }
    } catch (err) {
      saveError = message(err);
    }
  }

  async function refreshChats() {
    try {
      const r = await api.chats.listConversations({});
      chats = r.conversations;
      chatsError = '';
    } catch (err) {
      chatsError = message(err);
    } finally {
      chatsLoaded = true;
    }
  }

  // A ghost chat with turns is lost when another chat takes its place.
  async function leaveGhost(): Promise<boolean> {
    if (!ghost || session.turns.length === 0) return true;
    const yes = await confirm({ title: 'Discard this ghost chat?', message: 'It was never saved.', action: 'Discard', tone: 'bad' });
    if (yes) forget(fileIds(session.turns));
    return yes;
  }

  // Loads a saved conversation, leaving the current one as it was saved.
  async function openChat(id: string) {
    if (id === convId || busy) return;
    if (!(await leaveGhost())) return;
    opening = id;
    try {
      const r = await api.chats.getConversation({ id });
      if (!r.conversation) throw new Error('the daemon returned no conversation');
      const c = r.conversation;
      stop();
      session = fromConversation(c);
      savedSnapshot = JSON.stringify(session);
      convId = c.id;
      convTitle = c.title;
      saved = true;
      saveError = '';
      ghost = false;
      pending = [];
      lastSent = null;
      shownTrace = lastAnswer?.trace ?? '';
      if (c.model && live.routes.has(c.model)) model = c.model;
      upsertChat({ ...c, turns: [], settings: undefined });
      scroll();
      composer?.focus();
    } catch (err) {
      fail(err, 'Could not open the chat');
    } finally {
      opening = '';
    }
  }

  // Starts an empty conversation with the same settings. The current one stays saved.
  function newChat() {
    stop();
    session = fresh(session);
    savedSnapshot = '';
    convId = newId();
    convTitle = '';
    saved = false;
    saveError = '';
    lastSent = null;
    shownTrace = '';
    composer?.focus();
  }

  async function startNew() {
    if (await leaveGhost()) newChat();
  }

  // Ghost chats are never sent to the daemon. Turning ghost mode on or off
  // starts a new chat, since a saved chat cannot be unsaved.
  async function toggleGhost() {
    if (!(await leaveGhost())) return;
    ghost = !ghost;
    newChat();
    ok(ghost ? 'Ghost mode on' : 'Ghost mode off', ghost ? 'This chat is not saved and is gone when you leave' : 'Chats are saved to your account again');
  }

  async function deleteChat(c: Conversation) {
    const yes = await confirm({ title: `Delete "${c.title}"?`, message: 'The conversation and its images are removed from your account.', action: 'Delete', tone: 'bad' });
    if (!yes) return;
    try {
      await api.chats.deleteConversation({ id: c.id });
      chats = chats.filter((x) => x.id !== c.id);
      if (c.id === convId) {
        forget(fileIds(session.turns));
        saved = false;
        newChat();
      }
      ok(`Deleted ${c.title}`);
    } catch (err) {
      fail(err, 'Delete failed');
    }
  }

  async function renameChat(id: string, title: string) {
    try {
      const r = await api.chats.renameConversation({ id, title });
      if (r.conversation) {
        chats = chats.map((x) => (x.id === id ? { ...x, title: r.conversation!.title, updatedAt: r.conversation!.updatedAt } : x));
        if (id === convId) convTitle = r.conversation.title;
      }
    } catch (err) {
      fail(err, 'Rename failed');
    }
  }

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
    const wanted = page.url.searchParams.get('chat');
    if (wanted) openChat(wanted);
    else convId = newId();
    refreshChats();
  });
  onDestroy(() => {
    for (const u of urls.values()) URL.revokeObjectURL(u);
  });
  // Keep the model and the saved chat in the address for reloads and links.
  $effect(() => {
    const name = chosenName;
    const id = saved ? convId : '';
    const url = new URL(page.url);
    let changed = false;
    if (name && url.searchParams.get('model') !== name) {
      url.searchParams.set('model', name);
      changed = true;
    }
    if ((url.searchParams.get('chat') ?? '') !== id) {
      if (id) url.searchParams.set('chat', id);
      else url.searchParams.delete('chat');
      changed = true;
    }
    if (changed) replaceState(url, {});
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

  // Cache object URLs to avoid rereading stored files. Chat images come from
  // this browser's cache, then from the daemon. Generated media is browser only.
  $effect(() => {
    const chatIds = [...fileIds(session.turns), ...pending.map((a) => a.id)];
    const mediaIds = shown.flatMap((g) => g.files.map((f) => f.id));
    for (const id of [...chatIds, ...mediaIds]) {
      if (urls.has(id) || missing.has(id) || loadingIds.has(id)) continue;
      loadingIds.add(id);
      (mediaIds.includes(id) ? getImage(id) : loadFile(id))
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

  // Removes images from this browser and, for saved chats, from the daemon.
  function drop(ids: string[]) {
    forget(ids);
    if (!ghost && ids.length) deleteFiles(ids).catch((err) => fail(err, 'Could not remove an image from your account'));
  }

  async function clear() {
    if (media) {
      stop();
      for (const g of shown) remove(g);
      lastSent = null;
      shownTrace = '';
      composer?.focus();
      return;
    }
    if (saved) {
      const yes = await confirm({ title: `Delete "${convTitle || previewTitle(session.turns)}"?`, message: 'The conversation and its images are removed from your account.', action: 'Delete', tone: 'bad' });
      if (!yes) return;
      try {
        await api.chats.deleteConversation({ id: convId });
        chats = chats.filter((x) => x.id !== convId);
      } catch (err) {
        fail(err, 'Delete failed');
        return;
      }
    }
    forget(fileIds(session.turns));
    saved = false;
    newChat();
  }

  function removeTurn(i: number) {
    drop(fileIds([session.turns[i]]));
    session.turns = session.turns.filter((_, j) => j !== i);
  }

  // Exclude unanswered turns from history. Tool turns answer the calls of the assistant turn before them.
  function conversation(upTo = session.turns.length): Outgoing[] {
    const out: Outgoing[] = [];
    const past = session.turns.slice(0, upTo);
    for (let i = 0; i < past.length; i++) {
      const t = past[i];
      if (t.role === 'user') {
        const answer = past[i + 1];
        const answered = answer?.role === 'assistant' && (answer.text || answer.toolCalls?.length) && !answer.error;
        if (answered || i === past.length - 1) out.push({ role: 'user', content: t.text, attachments: t.images });
        continue;
      }
      if (t.role === 'assistant') {
        if ((t.text || t.toolCalls?.length) && !t.error) out.push({ role: 'assistant', content: t.text, toolCalls: t.toolCalls?.length ? t.toolCalls : undefined });
        continue;
      }
      out.push({ role: 'tool', content: t.text, toolCallId: t.toolCallId });
    }
    return out;
  }

  // The gateway's tools the last answer asked to run, with whether a tool turn answered each
  function pendingCalls(i: number): { call: ToolCall; answered: boolean }[] {
    const t = session.turns[i];
    if (t?.role !== 'assistant' || !session.approveTools) return [];
    const known = gatewayTools();
    const answered = new Set(session.turns.slice(i + 1).filter((x) => x.role === 'tool').map((x) => x.toolCallId));
    return (t.toolCalls ?? []).filter((c) => known.has(c.name)).map((c) => ({ call: c, answered: answered.has(c.id) }));
  }

  // Answers one of the last answer's calls, running it or declining it, and asks again once every call is answered
  async function settleCall(i: number, c: ToolCall, run: boolean) {
    let text = 'The user declined to run this tool.';
    if (run) {
      try {
        const r = await api.tools.callTool({ name: c.name, arguments: c.arguments });
        text = r.parts.map((p) => (p.type === 'image' ? `[image ${p.mediaType}]` : p.text)).join('\n') || 'The tool answered with nothing.';
        if (r.failed) text = 'The tool failed: ' + text;
      } catch (err) {
        text = 'The tool could not be called: ' + message(err);
      }
    }
    const after = session.turns.slice(i + 1).filter((x) => x.role === 'tool');
    session.turns = [...session.turns.slice(0, i + 1), ...after, { role: 'tool', text, toolCallId: c.id }];
    if (pendingCalls(i).every((p) => p.answered)) ask(session.turns.length - 1);
  }

  async function encode(a: Attachment): Promise<{ mediaType: string; data: string }> {
    let data = encoded.get(a.id);
    if (data === undefined) {
      const blob = await loadFile(a.id);
      if (!blob) throw new Error(`${a.name} could not be loaded. Remove the turn it is in to continue.`);
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
    const isGhost = ghost;
    fetchImage(url)
      .then(async (blob) => {
        const a = await storeImage(blob, 'Image from ' + chosenName);
        if (!isGhost) await uploadFile(a, blob);
        return a;
      })
      .then((a) => {
        urls.set(a.id, url);
        set({ id: a.id });
      })
      .catch((err) => set({ error: err instanceof Error ? err.message : String(err) }));
  }

  // Asks for an answer to the turns up to index, a user turn or the last tool turn of an approval
  async function ask(index: number) {
    if (busy || !chosen || !chosenReady) return;
    drop(fileIds(session.turns.slice(index + 1)));
    session.turns = [...session.turns.slice(0, index + 1), { role: 'assistant', text: '', dialect: session.dialect, startedAt: Date.now() }];
    const turn = session.turns[session.turns.length - 1];
    busy = true;
    controller = new AbortController();
    const calls = new Map<number, ToolCall>();
    scroll();
    try {
      const messages = await wire(conversation(index + 1));
      await send(
        { base: gatewayBase, dialect: session.dialect, model: chosen.name, messages, system: session.system.trim(), sampling, stream: session.stream, tools, key, headers: { ...toolHeaders(session.toolServers, session.mutatingTools, session.approveTools), ...(ghost ? ghostHeaders : {}) }, signal: controller.signal },
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
      composer?.focus();
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
        const { images, format } = await generateImages(gatewayBase, entry.request, key, ghost, controller.signal, (sent) => {
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
        const video = await createVideo(gatewayBase, entry.request, key, ghost, controller.signal, (sent) => {
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
      composer?.focus();
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
      composer?.grow();
      composer?.focus();
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

  function submit() {
    if (!canSend || !chosen) return;
    const text = draft.trim();
    const images = pending;
    pending = [];
    draft = '';
    composer?.reset();
    if (media) {
      generate(text, images[0] ?? null, mode === 'video' ? (images[1] ?? null) : null);
      forget(images.slice(mode === 'video' ? 2 : 1).map((a) => a.id));
      return;
    }
    session.turns = [...session.turns, { role: 'user', text, images: images.length ? images : undefined }];
    if (!ghost) {
      for (const a of images) {
        getImage(a.id)
          .then((blob) => {
            if (!blob) throw new Error(`${a.name} is not in this browser any more`);
            return uploadFile(a, blob);
          })
          .catch((err) => fail(err, `Could not save ${a.name} with the chat`));
      }
    }
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
    composer?.focus();
  }

  function unattach(a: Attachment) {
    pending = pending.filter((p) => p.id !== a.id);
    forget([a.id]);
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
        promptTokens = await countTokens(gatewayBase, name, sys.trim(), await wire(messages), key, ghost, c.signal);
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

  const emptyTurns = $derived(media ? shown.length === 0 : session.turns.length === 0);
  const hasTurns = $derived(!emptyTurns);
  const heading = $derived(ghost ? 'Ghost chat' : convTitle || (session.turns.length ? previewTitle(session.turns) : 'New chat'));

  function openTrace(trace: string) {
    shownTrace = trace;
    pane = 'trace';
    inspector = true;
  }
</script>

<svelte:head><title>Chat · nebu</title></svelte:head>

{#if loading || routes.length === 0}
  <PageHeader title="Chat" />
{:else}
  <PageHeader title="Chat">
    {#snippet meta()}
      {#if ghost}
        <span class="inline-flex items-center gap-1.5 rounded-full border border-line bg-raised/60 px-2 py-0.5 text-xs text-fg" title="Not saved, gone when you leave the page"><Ghost size={12} class="text-accent" />Ghost mode</span>
      {:else if saveError}
        <span class="text-warn" title={saveError}>Not saved: {saveError}</span>
      {:else}
        <span class="max-w-md truncate" title={heading}>{heading}</span>
        {#if saved}<span class="text-fg-faint">saved</span>{/if}
      {/if}
    {/snippet}
    <IconButton size="sm" icon={Ghost} variant={ghost ? 'primary' : 'secondary'} aria-pressed={ghost} label={ghost ? 'Ghost mode on. Turn it off to save chats again.' : 'Ghost mode: a chat that is never saved'} onclick={toggleGhost} />
    <Button size="sm" variant="primary" icon={MessageSquarePlus} onclick={startNew} disabled={busy}>New chat</Button>
  </PageHeader>
{/if}

{#if loading}
  <div class="h-[calc(100vh-8rem)]" aria-busy="true"></div>
{:else if routes.length === 0}
  <Empty icon={MessageSquare} title="Run a model to chat or make images and video.">
    {#if live.models.size}<Button variant="primary" href="/store">Library</Button>{:else}<Button variant="primary" href="/catalog">Browse catalog</Button>{/if}
  </Empty>
{:else}
  <div class="flex h-[calc(100vh-8rem)] min-h-[32rem] gap-4 lg:h-[calc(100vh-6rem)]">
    <section class="card relative flex min-w-0 flex-1 flex-col" aria-label="Conversation" ondragenter={onDragEnter} ondragover={onDragOver} ondragleave={onDragLeave} ondrop={onDrop}>
      {#if dropping}
        <div class="pointer-events-none absolute inset-0 z-20 flex items-center justify-center rounded-lg border-2 border-dashed bg-bg/80 {blind ? 'border-warn' : 'border-accent'}">
          <div class="flex items-center gap-2 text-sm {blind ? 'text-warn' : 'text-fg'}">
            {#if blind}<ImageOff size={18} />{blindNote}{:else}<ImagePlus size={18} />{media ? (mode === 'video' && pending.length ? 'Drop the last frame' : 'Drop an image to start from') : 'Drop to attach'}{/if}
          </div>
        </div>
      {/if}
      <ChatHeader bind:model bind:mode bind:inspector {modelItems} {chosen} {media} {canImage} {canVideo} {modeItems} {instance} {install} {slot} {vision} {hasTurns} {busy} {saved} onregenerate={regenerate} onclear={clear} />

      {#if chosen && !chosenReady}
        <div class="note note-warn m-4 mb-0">{chosen.name} is {enumLabel(RouteState, chosen.state)}. Requests wait until it is ready.</div>
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
              {#if ghost && !media}
                <div class="inline-flex items-center gap-1.5 text-xs text-fg-faint"><Ghost size={12} />Not saved</div>
              {/if}
              {#if media}
                <div class="text-xs text-fg-faint">{canVideo && canImage ? 'Describe an image or a video. Paste or drop an image to start from it.' : canVideo ? 'Describe a video. Paste or drop an image to start from it.' : 'Describe an image. Paste or drop an image to start from it.'}</div>
              {:else if vision}
                <div class="text-xs text-fg-faint">Paste, drop, or attach an image to ask about it</div>
              {/if}
            </div>
          {/if}
          {#if media}
            <MediaHistory {shown} {urls} {missing} onremove={remove} oncancel={cancel} onreuse={reuse} ondownload={download} onshow={show} ontrace={openTrace} />
          {:else}
            <ChatTurns turns={session.turns} {urls} {missing} {busy} {pendingCalls} onremove={removeTurn} onsettle={settleCall} onshow={show} ontrace={openTrace} />
          {/if}
        </div>
      </div>

      <Composer bind:this={composer} bind:draft {pending} {attaching} {busy} {canSend} {blind} {attachNote} {dropping} {media} {mode} modelName={chosen?.name ?? ''} {urls} onsubmit={submit} onstop={stop} onattach={attach} onunattach={unattach} onshow={show}>
        {#if media}
          <span class="font-mono">{mode}</span>
          {#each applied as s (s)}<span class="tabular-nums">{s}</span>{/each}
          {#if init}<span>{mode === 'video' ? 'image to video' : 'image to image'}</span>{/if}
          {#if last}<span>with a last frame</span>{/if}
          {#if busyHere && !busy}<span>a video is still being made</span>{/if}
        {:else}
          <span class="font-mono">{session.dialect}{session.stream ? ' · stream' : ''}</span>
          {#if promptTokens !== null}<span title="Counted by the gateway with the runtime's tokenizer">{promptTokens} prompt tokens{instance?.params.n_ctx ? ` of ${instance.params.n_ctx}` : ''}</span>{:else if countError}<span class="text-warn" title={countError}>token count unavailable</span>{/if}
        {/if}
      </Composer>
    </section>

    {#if inspector}
      <ChatInspector bind:pane tabs={inspectorTabs} padded={pane !== 'chats'}>
        {#if pane === 'chats'}
          <ChatList {chats} current={ghost ? '' : convId} loaded={chatsLoaded} error={chatsError} {opening} onopen={openChat} onrename={renameChat} ondelete={deleteChat} />
        {:else if pane === 'settings'}
          <ChatSettings bind:session bind:form bind:key {media} {mode} {caps} hasInit={!!init} {toolsProblem} needsKey={!!status?.auth && !auth.user} onkey={() => setGatewayKey(key.trim())} {gatewayBase} />
        {:else if pane === 'request'}
          <RequestPane sent={lastSent} />
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
      </ChatInspector>
    {/if}
  </div>
  <Lightbox bind:src={lightbox} alt={lightboxAlt} />
{/if}
