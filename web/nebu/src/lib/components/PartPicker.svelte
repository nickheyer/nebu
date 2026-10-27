<script lang="ts">
  import { Popover } from 'bits-ui';
  import { Check, ChevronDown, FolderOpen, Search, X } from '@lucide/svelte';
  import type { Param } from '$proto/runtime_pb';
  import type { StoredModel } from '$proto/store_pb';
  import type { Part } from '$proto/estimate_pb';
  import { bundled, candidates, describe, type Candidate } from '$lib/parts';
  import { isStoreRef } from '$lib/diffusion';
  import { storage, tail } from '$lib/format';
  import DirBrowser from './ui/DirBrowser.svelte';

  // Picks a stored part, any stored file, or a host path for a path parameter, auto leaving it to the planner
  let {
    value = $bindable(''),
    id,
    param,
    model = null,
    auto = '',
    inherited = false,
    missing,
    onchange
  }: { value?: string; id?: string; param: Param; model?: StoredModel | null; auto?: string; inherited?: boolean; missing?: Part; onchange?: (v: string) => void } = $props();

  let open = $state(false);
  let query = $state('');
  let showAll = $state(false);
  let browsing = $state(false);

  const lists = $derived(candidates(param, model));
  const q = $derived(query.trim().toLowerCase());
  const matches = (c: Candidate) => !q || `${c.label} ${c.reason} ${c.heading} ${c.path}`.toLowerCase().includes(q);
  const suggested = $derived(lists.suggested.filter(matches));
  const others = $derived(lists.others.filter(matches));
  const othersShown = $derived(showAll || q.length > 0);
  const chosen = $derived(describe(value, param));
  const beneath = $derived(auto ? describe(auto, param) : undefined);
  const own = $derived(bundled(model, param));
  const autoDetail = $derived(beneath ? beneath.label : own ? `bundled ${own}` : '');
  const none = $derived(value.trim().toLowerCase() === 'none');
  // Typed text with a separator is a path, not a search
  const typedPath = $derived(/[\\/]/.test(query.trim()));
  const name = $derived(param.label || param.name);
  const grouped = $derived.by(() => {
    const out: { heading: string; items: Candidate[] }[] = [];
    for (const c of others) {
      let g = out.find((x) => x.heading === c.heading);
      if (!g) out.push((g = { heading: c.heading, items: [] }));
      g.items.push(c);
    }
    return out;
  });

  function pick(v: string) {
    value = v;
    onchange?.(v);
    open = false;
  }
  function dirOf(path: string): string {
    const i = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'));
    return i > 0 ? path.slice(0, i) : '';
  }
</script>

{#snippet row(v: string, label: string, detail: string, on: boolean)}
  <button type="button" class="flex w-full items-center gap-2 rounded-[5px] px-2 py-1.5 text-left text-sm outline-none hover:bg-raised {on ? 'text-fg' : 'text-fg-muted'}" onclick={() => pick(v)}>
    <span class="min-w-0 flex-1 truncate">{label}{#if detail}<span class="ml-1.5 text-xs text-fg-faint">{detail}</span>{/if}</span>
    {#if on}<Check size={13} class="shrink-0 text-accent" />{/if}
  </button>
{/snippet}

{#snippet candidate(c: Candidate, why: boolean)}
  {@const on = value === c.value}
  <button type="button" class="flex w-full items-center gap-2 rounded-[5px] px-2 py-1.5 text-left outline-none hover:bg-raised" title={c.path || c.value} onclick={() => pick(c.value)}>
    <span class="flex min-w-0 flex-1 flex-col">
      <span class="truncate font-mono text-[13px] {on ? 'text-fg' : 'text-fg-muted'}">{c.label}</span>
      {#if why && (c.reason || c.path)}<span class="truncate text-xs text-fg-faint">{c.reason}{#if c.path && c.path !== c.value} · {tail(c.path)}{/if}</span>{/if}
    </span>
    {#if c.sizeBytes > 0n}<span class="shrink-0 text-xs tabular-nums text-fg-faint">{storage(c.sizeBytes)}</span>{/if}
    {#if on}<Check size={13} class="shrink-0 text-accent" />{/if}
  </button>
{/snippet}

<div class="flex min-w-0 items-center gap-1.5">
  <Popover.Root
    bind:open
    onOpenChange={(v) => {
      if (!v) {
        query = '';
        browsing = false;
      }
    }}
  >
    <Popover.Trigger {id} class="input flex min-w-0 flex-1 items-center gap-2 text-left data-[state=open]:border-accent">
      {#if chosen}
        <span class="min-w-0 flex-1 truncate font-mono {none ? 'text-fg-muted' : 'text-fg'}">{chosen.label}</span>
        {#if chosen.detail}<span class="hidden min-w-0 max-w-[40%] truncate text-xs text-fg-faint sm:inline">{chosen.detail}</span>{/if}
        {#if chosen.sizeBytes > 0n}<span class="shrink-0 text-xs tabular-nums text-fg-faint">{storage(chosen.sizeBytes)}</span>{/if}
      {:else if param.solved}
        <span class="shrink-0 text-fg-muted">Auto</span>
        {#if beneath}
          <span class="min-w-0 flex-1 truncate font-mono text-xs text-fg">{beneath.label}</span>
          {#if beneath.sizeBytes > 0n}<span class="shrink-0 text-xs tabular-nums text-fg-faint">{storage(beneath.sizeBytes)}</span>{/if}
        {:else if own}
          <span class="min-w-0 flex-1 truncate text-xs text-fg">bundled {own}</span>
        {:else if missing}
          <span class="min-w-0 flex-1 truncate text-xs text-warn">none stored</span>
        {:else}
          <span class="flex-1"></span>
        {/if}
      {:else if beneath}
        <span class="shrink-0 text-fg-muted">{inherited ? 'Slot' : 'Default'}</span>
        <span class="min-w-0 flex-1 truncate font-mono text-xs text-fg">{beneath.label}</span>
      {:else}
        <span class="flex-1 text-fg-faint">Not set</span>
      {/if}
      <ChevronDown size={14} class="shrink-0 text-fg-faint" />
    </Popover.Trigger>
    <Popover.Portal>
      <Popover.Content sideOffset={4} align="start" class="enter-up z-[80] flex w-[min(36rem,calc(100vw-2rem))] flex-col overflow-hidden rounded-md border border-line bg-overlay shadow-pop focus:outline-none">
        <div class="flex items-center gap-2 border-b border-line px-2.5">
          <Search size={14} class="shrink-0 text-fg-faint" />
          <input
            class="h-9 min-w-0 flex-1 bg-transparent font-mono text-sm text-fg placeholder:text-fg-faint focus:outline-none"
            placeholder="Search or type a path"
            aria-label="Search files for {name}"
            autocomplete="off"
            spellcheck="false"
            bind:value={query}
            onkeydown={(e) => {
              if (e.key === 'Enter' && typedPath) {
                e.preventDefault();
                pick(query.trim());
              }
            }}
          />
        </div>
        <div class="max-h-[22rem] overflow-y-auto p-1">
          {#if typedPath}
            {@render row(query.trim(), query.trim(), '', value === query.trim())}
          {/if}
          {#if !q}
            {#if param.solved}
              {@render row('', 'Auto', autoDetail, value === '')}
              {@render row('none', 'None', '', none)}
            {:else}
              {@render row('', 'Not set', inherited && beneath ? beneath.label : '', value === '')}
            {/if}
          {/if}
          <div class="caps flex items-baseline gap-1.5 px-2 pt-2 pb-1 text-fg-faint">Suggested{#if suggested.length}<span class="font-normal tracking-normal normal-case">{suggested.length}</span>{/if}</div>
          {#each suggested as c (c.value)}
            {@render candidate(c, true)}
          {:else}
            <p class="px-2 py-1.5 text-xs text-fg-faint">{q ? 'No matches' : 'None stored'}</p>
          {/each}
          {#if othersShown}
            <div class="caps flex items-baseline gap-1.5 px-2 pt-3 pb-1 text-fg-faint">More files<span class="font-normal tracking-normal normal-case">{others.length}</span></div>
            {#each grouped as g (g.heading)}
              <div class="truncate px-2 pt-1.5 pb-0.5 text-xs text-fg-muted">{g.heading}</div>
              {#each g.items as c (c.value)}{@render candidate(c, false)}{/each}
            {:else}
              <p class="px-2 py-1.5 text-xs text-fg-faint">No matches</p>
            {/each}
          {:else if lists.others.length}
            <button type="button" class="mt-1 flex w-full items-center gap-2 rounded-[5px] px-2 py-1.5 text-left text-sm text-fg-muted hover:bg-raised hover:text-fg" onclick={() => (showAll = true)}>
              More files…<span class="text-xs tabular-nums text-fg-faint">{lists.others.length}</span>
            </button>
          {/if}
        </div>
        <div class="border-t border-line p-1">
          <button type="button" class="flex w-full items-center gap-2 rounded-[5px] px-2 py-1.5 text-left text-sm text-fg-muted hover:bg-raised hover:text-fg" aria-expanded={browsing} onclick={() => (browsing = !browsing)}>
            <FolderOpen size={14} class="shrink-0" />
            Browse host…
          </button>
          {#if browsing}
            <div class="p-1"><DirBrowser start={value && !isStoreRef(value) ? dirOf(value) : ''} mode="files" onpick={pick} /></div>
          {/if}
        </div>
      </Popover.Content>
    </Popover.Portal>
  </Popover.Root>
  {#if value}
    <button type="button" class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md text-fg-faint transition-colors hover:bg-raised hover:text-fg" aria-label="Clear {name}" onclick={() => pick('')}><X size={14} /></button>
  {/if}
</div>
