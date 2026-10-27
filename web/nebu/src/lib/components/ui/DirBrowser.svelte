<script lang="ts">
  import { onMount } from 'svelte';
  import { api, message } from '$lib/api';
  import type { DirEntry } from '$proto/host_pb';
  import { storage } from '$lib/format';
  import { ArrowUp, Folder } from '@lucide/svelte';
  import Spinner from './Spinner.svelte';

  // Lists host directories so one entry can be picked, every file or executables alone
  let { start = '', mode = 'files', onpick, class: cls = '' }: { start?: string; mode?: 'files' | 'executables'; onpick: (path: string) => void; class?: string } = $props();

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

  export async function load(at: string) {
    loading = true;
    error = '';
    try {
      const r = await api.host.listDirectory({ path: at });
      path = r.path;
      parent = r.parent;
      entries = r.entries.filter((e) => e.dir || mode === 'files' || e.executable);
    } catch (err) {
      error = message(err);
    } finally {
      loading = false;
    }
  }

  function pick(e: DirEntry) {
    const full = path.endsWith(sep) ? path + e.name : path + sep + e.name;
    if (e.dir) {
      void load(full);
      return;
    }
    onpick(full);
  }

  onMount(() => {
    void load(start);
  });
</script>

<div class="overflow-hidden rounded-md border border-line bg-sunken {cls}">
  <div class="flex min-h-8 items-center gap-0.5 overflow-x-auto border-b border-line px-1.5 font-mono text-xs whitespace-nowrap">
    <button type="button" class="mr-1 flex h-6 w-6 shrink-0 items-center justify-center rounded-sm text-fg-muted hover:bg-raised hover:text-fg disabled:opacity-40" aria-label="Up" disabled={!parent} onclick={() => load(parent)}><ArrowUp size={13} /></button>
    {#if path.startsWith(sep)}<span class="text-fg-faint">{sep}</span>{/if}
    {#each crumbs as c, i (c.at)}
      {#if i}<span class="text-fg-faint">{sep}</span>{/if}
      <button type="button" class="shrink-0 rounded-sm px-1 py-0.5 text-fg-muted hover:bg-raised hover:text-fg" onclick={() => load(c.at)}>{c.name}</button>
    {/each}
    {#if loading}<Spinner size={12} class="mr-1 ml-auto text-fg-faint" />{/if}
  </div>
  {#if error}
    <p class="px-3 py-2 text-sm text-bad">{error}</p>
  {:else}
    <ul class="max-h-64 overflow-y-auto py-1">
      {#each entries as e (e.name)}
        <li>
          <button type="button" class="flex w-full items-center gap-2 px-3 py-1 text-left text-sm hover:bg-raised {e.dir ? 'text-fg-muted' : 'font-mono text-fg'}" onclick={() => pick(e)}>
            {#if e.dir}<Folder size={13} class="shrink-0 text-fg-faint" />{:else}<span class="w-[13px] shrink-0"></span>{/if}
            <span class="min-w-0 flex-1 truncate">{e.name}</span>
            {#if !e.dir && e.sizeBytes > 0n}<span class="shrink-0 font-sans text-xs tabular-nums text-fg-faint">{storage(e.sizeBytes)}</span>{/if}
          </button>
        </li>
      {:else}
        {#if !loading}<li class="px-3 py-2 text-sm text-fg-faint">Empty</li>{/if}
      {/each}
    </ul>
  {/if}
</div>
