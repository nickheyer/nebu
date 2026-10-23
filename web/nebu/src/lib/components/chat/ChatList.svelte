<script lang="ts">
  import { Pencil, Trash2, Check } from '@lucide/svelte';
  import { clock } from '$lib/state.svelte';
  import { ago } from '$lib/format';
  import type { Conversation } from '$proto/chat_pb';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import TextInput from '$lib/components/ui/TextInput.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';

  let {
    chats,
    current,
    loaded,
    error,
    opening,
    onopen,
    onrename,
    ondelete
  }: {
    chats: Conversation[];
    // The id of the chat on screen, empty for a ghost chat
    current: string;
    loaded: boolean;
    error: string;
    // The id being opened, empty otherwise
    opening: string;
    onopen: (id: string) => void;
    onrename: (id: string, title: string) => Promise<void>;
    ondelete: (c: Conversation) => void;
  } = $props();

  let renaming = $state('');
  let renameText = $state('');

  function startRename(c: Conversation) {
    renaming = c.id;
    renameText = c.title;
  }

  async function finishRename() {
    const id = renaming;
    const title = renameText.trim();
    renaming = '';
    if (!id || !title) return;
    await onrename(id, title);
  }
</script>

{#if !loaded}
  <div class="skeleton m-2 h-24" aria-busy="true"></div>
{:else if error}
  <div class="note note-bad m-2 text-xs">{error}</div>
{:else if chats.length === 0}
  <p class="px-2 py-8 text-center text-sm text-fg-faint">Chats appear here once a model answers.</p>
{:else}
  <div class="flex flex-col gap-0.5">
    {#each chats as c (c.id)}
      {@const on = c.id === current}
      <div class="group relative rounded-md {on ? 'bg-raised/70' : 'hover:bg-raised/40'}">
        {#if renaming === c.id}
          <div class="flex items-center gap-1 p-1.5">
            <TextInput size="sm" class="flex-1" bind:value={renameText} onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); finishRename(); } else if (e.key === 'Escape') renaming = ''; }} onblur={finishRename} autofocus />
            <IconButton size="xs" icon={Check} label="Save the name" onclick={finishRename} />
          </div>
        {:else}
          <button type="button" class="block w-full px-2.5 py-2 text-left" onclick={() => onopen(c.id)} disabled={opening === c.id} aria-current={on ? 'true' : undefined}>
            <div class="truncate pr-14 text-sm {on ? 'text-fg' : 'text-fg-muted group-hover:text-fg'}">{c.title || 'Untitled'}</div>
            <div class="mt-0.5 flex items-center gap-1.5 truncate text-[11px] text-fg-faint"><span class="truncate font-mono">{c.model}</span><span>·</span><span class="shrink-0">{ago(c.updatedAt, clock.now)}</span>{#if opening === c.id}<Spinner size={10} class="ml-auto shrink-0" />{/if}</div>
          </button>
          <span class="absolute top-1.5 right-1.5 flex opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100">
            <IconButton size="xs" icon={Pencil} label="Rename" onclick={() => startRename(c)} />
            <IconButton size="xs" icon={Trash2} label="Delete" onclick={() => ondelete(c)} />
          </span>
        {/if}
      </div>
    {/each}
  </div>
{/if}
