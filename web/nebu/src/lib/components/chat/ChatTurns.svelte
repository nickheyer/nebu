<script lang="ts">
  import type { SvelteMap, SvelteSet } from 'svelte/reactivity';
  import { X, Wrench, Check, ImageOff, FileJson } from '@lucide/svelte';
  import { live } from '$lib/state.svelte';
  import { ms, rate, millisBetween } from '$lib/format';
  import { stopNote, type Turn } from '$lib/chats';
  import type { ToolCall } from '$lib/chatClient';
  import Button from '$lib/components/ui/Button.svelte';
  import Markdown from '$lib/components/ui/Markdown.svelte';
  import Copy from '$lib/components/ui/Copy.svelte';
  import Json from '$lib/components/ui/Json.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';
  import Logo from '$lib/components/Logo.svelte';
  import Thumb from './Thumb.svelte';

  let {
    turns,
    urls,
    missing,
    busy,
    pendingCalls,
    onremove,
    onsettle,
    onshow,
    ontrace
  }: {
    turns: Turn[];
    urls: SvelteMap<string, string>;
    missing: SvelteSet<string>;
    busy: boolean;
    // The gateway's tools the last answer asked to run, with whether a tool turn answered each
    pendingCalls: (i: number) => { call: ToolCall; answered: boolean }[];
    onremove: (i: number) => void;
    onsettle: (i: number, call: ToolCall, run: boolean) => void;
    onshow: (url: string | undefined, alt: string) => void;
    ontrace: (trace: string) => void;
  } = $props();
</script>

{#each turns as t, i (i)}
  {#if t.role === 'tool'}
    <div class="group flex items-start gap-2 pl-8">
      <div class="min-w-0 flex-1 rounded-md border border-line bg-sunken px-3 py-2 text-xs text-fg-muted">
        <div class="mb-1 flex items-center gap-2"><Wrench size={11} class="text-fg-faint" /><span class="font-mono text-fg-faint">{t.toolCallId}</span></div>
        <pre class="max-h-40 overflow-auto whitespace-pre-wrap">{t.text}</pre>
      </div>
      <button type="button" class="mt-2 rounded-md p-1 text-fg-faint opacity-0 transition-opacity group-hover:opacity-100 hover:bg-raised hover:text-fg" aria-label="Remove" onclick={() => onremove(i)}><X size={13} /></button>
    </div>
  {:else if t.role === 'user'}
    <div class="group flex items-start justify-end gap-2">
      <button type="button" class="mt-2 rounded-md p-1 text-fg-faint opacity-0 transition-opacity group-hover:opacity-100 hover:bg-raised hover:text-fg" aria-label="Remove" onclick={() => onremove(i)}><X size={13} /></button>
      <div class="max-w-[85%] rounded-xl rounded-br-sm bg-raised px-4 py-2.5 text-sm text-fg">
        {#if t.images?.length}
          <div class="flex flex-wrap justify-end gap-2 {t.text ? 'mb-2' : ''}">
            {#each t.images as a (a.id)}<Thumb {a} size="h-24 w-32" url={urls.get(a.id)} missing={missing.has(a.id)} onopen={onshow} />{/each}
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
    {@const pending = i === turns.length - 1 ? pendingCalls(i) : []}
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
            <button type="button" class="mt-2 block overflow-hidden rounded-md border border-line" onclick={() => onshow(src, `Image ${mi + 1} from ${t.dialect ?? 'the answer'}`)} aria-label="Open image {mi + 1}">
              <img {src} alt="Image {mi + 1} of the answer" class="block h-auto max-h-96 w-auto max-w-full object-contain" loading="lazy" />
            </button>
          {:else}
            <div class="mt-2 flex h-24 w-32 items-center justify-center rounded-md border border-line"><Spinner size={14} class="text-fg-faint" /></div>
          {/if}
        {/each}
        {#each (trace?.toolCalls ?? []).filter((c) => c.server) as c, ci (ci)}
          <div class="card mt-2 p-3">
            <div class="mb-2 flex flex-wrap items-center gap-2 text-xs"><Wrench size={12} class="text-accent" /><span class="font-mono text-fg">{c.name}</span><span class="text-fg-faint">ran through {c.server}{c.failed ? ', failed' : ''}{c.tookMs ? ` in ${ms(c.tookMs)}` : ''}</span></div>
            <Json text={c.arguments} height="max-h-32" empty="No arguments" />
            <pre class="code mt-2 max-h-40 overflow-auto text-xs whitespace-pre-wrap">{c.result || 'No answer'}</pre>
          </div>
        {/each}
        {#each t.toolCalls ?? [] as c, ci (ci)}
          {@const ask = pending.find((p) => p.call.id === c.id)}
          <div class="card mt-2 p-3">
            <div class="mb-2 flex items-center gap-2 text-xs"><Wrench size={12} class="text-accent" /><span class="font-mono text-fg">{c.name || 'tool'}</span>{#if c.id}<span class="font-mono text-fg-faint">{c.id}</span>{/if}</div>
            <Json text={c.arguments} height="max-h-48" empty="No arguments" />
            {#if ask && !ask.answered}
              <div class="mt-2 flex items-center gap-2">
                <span class="text-xs text-fg-muted">This tool changes something. Run it?</span>
                <Button size="sm" variant="primary" icon={Check} disabled={busy} onclick={() => onsettle(i, c, true)}>Run</Button>
                <Button size="sm" variant="ghost" icon={X} disabled={busy} onclick={() => onsettle(i, c, false)}>Decline</Button>
              </div>
            {/if}
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
              <button type="button" class="inline-flex items-center gap-1 text-fg-muted hover:text-fg" onclick={() => ontrace(t.trace!)}><FileJson size={12} /> trace</button>
            {/if}
            {#if t.text}<Copy text={t.text} size={12} class="h-6 w-6" />{/if}
          </div>
        {/if}
      </div>
    </div>
  {/if}
{/each}
