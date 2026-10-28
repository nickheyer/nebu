<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { SvelteMap } from 'svelte/reactivity';
  import { X, ImagePlus, Square, ArrowUp } from '@lucide/svelte';
  import { bytes } from '$lib/format';
  import type { Attachment } from '$lib/images';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';

  let {
    draft = $bindable(''),
    pending,
    attaching,
    busy,
    canSend,
    blind,
    attachNote,
    dropping,
    media,
    mode,
    modelName,
    speaker = '',
    urls,
    children,
    onsubmit,
    onstop,
    onattach,
    onunattach,
    onshow
  }: {
    draft?: string;
    pending: Attachment[];
    attaching: number;
    busy: boolean;
    canSend: boolean;
    blind: boolean;
    attachNote: string;
    dropping: boolean;
    media: boolean;
    mode: 'image' | 'video';
    // Empty when no model is picked
    modelName: string;
    // The character being spoken to, when the chat names one
    speaker?: string;
    urls: SvelteMap<string, string>;
    // The status line under the box
    children: Snippet;
    onsubmit: () => void;
    onstop: () => void;
    onattach: (files: File[]) => void;
    onunattach: (a: Attachment) => void;
    onshow: (url: string | undefined, alt: string) => void;
  } = $props();

  let box: HTMLTextAreaElement | undefined = $state();
  let picker: HTMLInputElement | undefined = $state();

  export function focus() {
    box?.focus();
  }

  // Fits the box to its text, up to about ten lines
  export function grow() {
    if (!box) return;
    box.style.height = '';
    box.style.height = Math.min(box.scrollHeight, 280) + 'px';
  }

  export function reset() {
    if (box) box.style.height = '';
  }

  const placeholder = $derived(
    !modelName ? 'Pick a model' : media ? (mode === 'video' ? `Describe a video for ${modelName}` : `Describe an image for ${modelName}`) : speaker.trim() ? `Say something to ${speaker.trim()}` : `Message ${modelName}`
  );

  function onKey(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      onsubmit();
    }
  }

  function onPick(e: Event) {
    const el = e.currentTarget as HTMLInputElement;
    onattach([...(el.files ?? [])]);
    el.value = '';
  }

  function onPaste(e: ClipboardEvent) {
    const files = [...(e.clipboardData?.files ?? [])].filter((f) => f.type.startsWith('image/'));
    if (!files.length) return;
    e.preventDefault();
    onattach(files);
  }
</script>

<div class="border-t border-line p-3">
  <div class="mx-auto flex max-w-3xl flex-col gap-1.5">
    <div class="rounded-xl border bg-sunken transition-colors focus-within:border-accent {dropping ? 'border-dashed border-accent' : 'border-line'}">
      {#if pending.length || attaching}
        <div class="flex flex-wrap gap-2 px-3 pt-3">
          {#each pending as a, ai (a.id)}
            <div class="relative">
              <button type="button" class="block overflow-hidden rounded-md border border-line" onclick={() => onshow(urls.get(a.id), a.name)} aria-label="Open {a.name}" title="{a.name} · {a.width}×{a.height} · {bytes(a.bytes)}">
                <img src={urls.get(a.id)} alt={a.name} class="block h-16 w-16 object-cover" />
              </button>
              {#if media}<span class="absolute bottom-0.5 left-0.5 rounded-sm bg-bg/80 px-1 text-[10px] text-fg-muted">{ai === 0 ? 'start' : 'last frame'}</span>{/if}
              <button type="button" class="absolute -top-1.5 -right-1.5 rounded-full border border-line bg-raised p-0.5 text-fg-muted transition-colors hover:bg-line-strong hover:text-fg" aria-label="Remove {a.name}" onclick={() => onunattach(a)}><X size={11} /></button>
            </div>
          {/each}
          {#each { length: attaching } as _, ai (ai)}
            <div class="flex h-16 w-16 items-center justify-center rounded-md border border-line" aria-label="Reading an image"><Spinner size={14} class="text-fg-faint" /></div>
          {/each}
        </div>
      {/if}
      <textarea
        bind:this={box}
        class="block max-h-[280px] w-full resize-none bg-transparent px-3.5 pt-3 pb-1 text-[15px] leading-6 text-fg placeholder:text-fg-faint focus:outline-none"
        rows="1"
        {placeholder}
        bind:value={draft}
        onkeydown={onKey}
        oninput={grow}
        onpaste={onPaste}
      ></textarea>
      <div class="flex items-center gap-1 px-1.5 pb-1.5">
        <IconButton icon={ImagePlus} label={attachNote} onclick={() => picker?.click()} disabled={blind || busy} />
        <input bind:this={picker} type="file" accept="image/*" multiple={!media} class="hidden" onchange={onPick} />
        <span class="ml-auto hidden items-center gap-1 text-xs text-fg-faint sm:inline-flex"><kbd class="kbd">Shift</kbd>+<kbd class="kbd">Enter</kbd> for a new line</span>
        {#if busy}
          <Button variant="danger" icon={Square} onclick={onstop} aria-label="Stop" />
        {:else}
          <Button variant="primary" icon={ArrowUp} onclick={onsubmit} disabled={!canSend} aria-label="Send" />
        {/if}
      </div>
    </div>
    <div class="flex flex-wrap items-center gap-x-3 px-1 text-xs text-fg-faint">
      {@render children()}
    </div>
  </div>
</div>
