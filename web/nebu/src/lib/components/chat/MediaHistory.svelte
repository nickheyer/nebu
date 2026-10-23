<script lang="ts">
  import type { SvelteMap, SvelteSet } from 'svelte/reactivity';
  import { X, Square, ImageOff, Film, Download, FileJson, Settings2 } from '@lucide/svelte';
  import { bytes } from '$lib/format';
  import { summaryOf, type Generation } from '$lib/generate';
  import type { Attachment } from '$lib/images';
  import Button from '$lib/components/ui/Button.svelte';
  import Copy from '$lib/components/ui/Copy.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';
  import Logo from '$lib/components/Logo.svelte';
  import Thumb from './Thumb.svelte';

  let {
    shown,
    urls,
    missing,
    onremove,
    oncancel,
    onreuse,
    ondownload,
    onshow,
    ontrace
  }: {
    shown: Generation[];
    urls: SvelteMap<string, string>;
    missing: SvelteSet<string>;
    onremove: (g: Generation) => void;
    oncancel: (g: Generation) => void;
    onreuse: (g: Generation) => void;
    ondownload: (f: Attachment) => void;
    onshow: (url: string | undefined, alt: string) => void;
    ontrace: (trace: string) => void;
  } = $props();
</script>

{#each shown as g (g.id)}
  <div class="group flex items-start justify-end gap-2">
    <button type="button" class="mt-2 rounded-md p-1 text-fg-faint opacity-0 transition-opacity group-hover:opacity-100 hover:bg-raised hover:text-fg" aria-label="Remove" onclick={() => onremove(g)}><X size={13} /></button>
    <div class="max-w-[85%] rounded-xl rounded-br-sm bg-raised px-4 py-2.5 text-sm text-fg">
      {#if g.inputs?.length}
        <div class="mb-2 flex flex-wrap justify-end gap-2">
          {#each g.inputs as a (a.id)}<Thumb {a} size="h-24 w-32" url={urls.get(a.id)} missing={missing.has(a.id)} onopen={onshow} />{/each}
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
          <span class="ml-auto"><Button size="sm" variant="danger" icon={Square} onclick={() => oncancel(g)}>Cancel</Button></span>
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
                <div class="flex h-40 w-56 flex-col items-center justify-center gap-2 rounded-md border border-line text-xs text-fg-muted"><Film size={18} /><span>AVI video · {bytes(f.bytes)}</span><Button size="sm" icon={Download} onclick={() => ondownload(f)}>Download</Button></div>
              {:else}
                <button type="button" class="block overflow-hidden rounded-md border border-line" onclick={() => onshow(url, g.request.prompt)} aria-label="Open image">
                  <img src={url} alt={g.request.prompt} class="block h-auto max-h-[28rem] w-auto max-w-full object-contain" loading="lazy" />
                </button>
              {/if}
              {#if url && !missing.has(f.id)}
                <button type="button" class="absolute top-2 right-2 rounded-md border border-line bg-raised/90 p-1.5 text-fg-muted opacity-0 transition-opacity group-hover/file:opacity-100 hover:text-fg" aria-label="Download" onclick={() => ondownload(f)}><Download size={13} /></button>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
      <div class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs tabular-nums text-fg-faint">
        {#each summaryOf(g) as s (s)}<span>{s}</span>{/each}
        <span>{new Date(g.createdAt).toLocaleString()}</span>
        {#if g.trace}
          <button type="button" class="inline-flex items-center gap-1 text-fg-muted hover:text-fg" onclick={() => ontrace(g.trace!)}><FileJson size={12} /> trace</button>
        {/if}
        <button type="button" class="inline-flex items-center gap-1 text-fg-muted hover:text-fg" onclick={() => onreuse(g)}><Settings2 size={12} /> reuse settings</button>
        <Copy text={g.request.prompt} size={12} class="h-6 w-6" label="Copy the prompt" />
      </div>
    </div>
  </div>
{/each}
