<script lang="ts">
  import { api, message } from '$lib/api';
  import type { DirEntry } from '$proto/host_pb';
  import { ArrowUp, Folder, FolderOpen } from '@lucide/svelte';
  import TextInput from './TextInput.svelte';
  import Button from './Button.svelte';
  import Spinner from './Spinner.svelte';

  let { value = $bindable(''), id, empty = '', class: cls = '' }: { value?: string; id?: string; empty?: string; class?: string } = $props();

  let open = $state(false);
  let path = $state('');
  let parent = $state('');
  let entries = $state<DirEntry[]>([]);
  let loading = $state(false);
  let error = $state('');

  const sep = $derived(path.includes('\\') && !path.includes('/') ? '\\' : '/');
  const crumbs = $derived.by(() => {
    const parts = path.split(sep).filter(Boolean);
    const lead = path.startsWith(sep) ? sep : '';
    return parts.map((name, i) => ({ name, at: lead + parts.slice(0, i + 1).join(sep) }));
  });

  async function load(at: string) {
    loading = true;
    error = '';
    try {
      const r = await api.host.listDirectory({ path: at });
      path = r.path;
      parent = r.parent;
      entries = r.entries.filter((e) => e.dir || e.executable);
    } catch (err) {
      error = message(err);
    } finally {
      loading = false;
    }
  }

  function browse() {
    open = !open;
    if (open) load(value.trim() || empty);
  }

  function pick(e: DirEntry) {
    const full = path.endsWith(sep) ? path + e.name : path + sep + e.name;
    if (e.dir) {
      load(full);
      return;
    }
    value = full;
    open = false;
  }
</script>

<div class="flex flex-col gap-2 {cls}">
  <div class="flex gap-2">
    <TextInput {id} mono class="flex-1" {empty} bind:value />
    <Button type="button" variant={open ? 'subtle' : 'secondary'} icon={FolderOpen} onclick={browse}>Browse</Button>
  </div>
  {#if open}
    <div class="overflow-hidden rounded-md border border-line bg-sunken">
      <div class="flex min-h-8 items-center gap-0.5 overflow-x-auto border-b border-line px-1.5 font-mono text-xs whitespace-nowrap">
        <button type="button" class="mr-1 flex h-6 w-6 shrink-0 items-center justify-center rounded-sm text-fg-muted hover:bg-raised hover:text-fg disabled:opacity-40" aria-label="Up" disabled={!parent} onclick={() => load(parent)}><ArrowUp size={13} /></button>
        {#if path.startsWith(sep)}<span class="text-fg-faint">{sep}</span>{/if}
        {#each crumbs as c, i (c.at)}
          {#if i}<span class="text-fg-faint">{sep}</span>{/if}
          <button type="button" class="shrink-0 rounded-sm px-1 py-0.5 text-fg-muted hover:bg-raised hover:text-fg" onclick={() => load(c.at)}>{c.name}</button>
        {/each}
        {#if loading}<Spinner size={12} class="ml-auto mr-1 text-fg-faint" />{/if}
      </div>
      {#if error}
        <p class="px-3 py-2 text-sm text-bad">{error}</p>
      {:else}
        <ul class="max-h-64 overflow-y-auto py-1">
          {#each entries as e (e.name)}
            <li>
              <button type="button" class="flex w-full items-center gap-2 px-3 py-1 text-left text-sm hover:bg-raised {e.dir ? 'text-fg-muted' : 'font-mono text-fg'}" onclick={() => pick(e)}>
                {#if e.dir}<Folder size={13} class="shrink-0 text-fg-faint" />{:else}<span class="w-[13px] shrink-0"></span>{/if}
                <span class="truncate">{e.name}</span>
              </button>
            </li>
          {:else}
            {#if !loading}<li class="px-3 py-2 text-sm text-fg-faint">Empty</li>{/if}
          {/each}
        </ul>
      {/if}
    </div>
  {/if}
</div>
