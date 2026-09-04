<script lang="ts">
  import { Popover } from 'bits-ui';
  import { ChevronDown, X, Search, Check } from '@lucide/svelte';
  import type { Facet } from '$proto/source_pb';
  import { groupValues, humanize, joinValues, splitValues } from '$lib/catalog';

  let { facet, value = '', onChange }: { facet: Facet; value?: string; onChange: (value: string) => void } = $props();

  let open = $state(false);
  let filter = $state('');
  let draft = $state('');

  const chosen = $derived(splitValues(value));
  const labelOf = (id: string) => facet.values.find((v) => v.id === id)?.label ?? humanize(id);
  const summary = $derived(chosen.length === 0 ? '' : chosen.length === 1 ? labelOf(chosen[0]) : `${labelOf(chosen[0])} +${chosen.length - 1}`);
  const searchable = $derived(facet.values.length > 8);
  const groups = $derived.by(() => {
    const q = filter.trim().toLowerCase();
    return groupValues(facet)
      .map((g) => ({ group: g.group, values: q ? g.values.filter((v) => v.label.toLowerCase().includes(q) || v.id.toLowerCase().includes(q)) : g.values }))
      .filter((g) => g.values.length);
  });

  function toggle(id: string) {
    if (facet.multi) {
      const next = chosen.includes(id) ? chosen.filter((c) => c !== id) : [...chosen, id];
      onChange(joinValues(next));
    } else {
      onChange(chosen[0] === id ? '' : id);
      open = false;
    }
  }
  function clear(e?: Event) {
    e?.stopPropagation();
    onChange('');
    draft = '';
  }
  function applyDraft() {
    onChange(draft.trim());
    open = false;
  }
  $effect(() => {
    if (open) {
      filter = '';
      draft = value;
    }
  });
</script>

<Popover.Root bind:open>
  <Popover.Trigger
    class="inline-flex h-8 max-w-[16rem] items-center gap-1.5 rounded-md border px-2.5 text-xs transition-colors {chosen.length
      ? 'border-accent/40 bg-accent/10 text-fg'
      : 'border-line bg-raised text-fg-muted hover:border-line-strong hover:text-fg'} data-[state=open]:border-accent/60"
  >
    <span class="shrink-0 {chosen.length ? 'text-fg-muted' : ''}">{facet.label}</span>
    {#if summary}<span class="truncate font-medium">{summary}</span>{/if}
    {#if chosen.length}
      <span role="button" tabindex="-1" class="ml-0.5 rounded p-0.5 text-fg-faint hover:bg-raised hover:text-fg" onclick={clear} onkeydown={(e) => e.key === 'Enter' && clear(e)} aria-label="Clear {facet.label}"><X size={11} /></span>
    {:else}
      <ChevronDown size={12} class="text-fg-faint" />
    {/if}
  </Popover.Trigger>
  <Popover.Portal>
    <Popover.Content align="start" sideOffset={6} class="enter-up z-[60] w-72 rounded-xl border border-line bg-overlay shadow-pop focus:outline-none">
      {#if facet.freeform}
        <form
          class="flex flex-col gap-2 p-3"
          onsubmit={(e) => {
            e.preventDefault();
            applyDraft();
          }}
        >
          <label class="text-xs font-medium text-fg-muted" for="facet-{facet.id}">{facet.label}</label>
          <input id="facet-{facet.id}" class="input h-8" bind:value={draft} placeholder="Type and press Enter" autocomplete="off" />
          <div class="flex justify-end gap-2">
            {#if value}<button type="button" class="text-xs text-fg-faint hover:text-fg" onclick={() => clear()}>Clear</button>{/if}
            <button type="submit" class="rounded-md bg-accent px-2.5 py-1 text-xs font-semibold text-accent-fg">Apply</button>
          </div>
        </form>
      {:else}
        {#if searchable}
          <div class="border-b border-line p-2">
            <div class="relative">
              <Search size={12} class="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-fg-faint" />
              <input class="input h-7 pl-7 text-xs" bind:value={filter} placeholder="Filter {facet.label.toLowerCase()}" autocomplete="off" />
            </div>
          </div>
        {/if}
        <div class="max-h-72 overflow-y-auto p-1">
          {#each groups as g (g.group)}
            {#if g.group}<div class="px-2 pt-2 pb-1 text-[10.5px] font-semibold tracking-wider text-fg-faint uppercase">{g.group}</div>{/if}
            {#each g.values as v (v.id)}
              {@const on = chosen.includes(v.id)}
              <button type="button" class="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-raised {on ? 'text-fg' : 'text-fg-muted'}" onclick={() => toggle(v.id)}>
                <span class="flex h-4 w-4 shrink-0 items-center justify-center rounded border {facet.multi ? '' : 'rounded-full'} {on ? 'border-accent bg-accent text-accent-fg' : 'border-line-strong'}">{#if on}<Check size={10} strokeWidth={3} />{/if}</span>
                <span class="truncate">{v.label}</span>
              </button>
            {/each}
          {:else}
            <div class="px-3 py-4 text-center text-xs text-fg-faint">No matches</div>
          {/each}
        </div>
        {#if chosen.length}
          <div class="border-t border-line px-3 py-2 text-right"><button type="button" class="text-xs text-fg-faint hover:text-fg" onclick={() => clear()}>Clear</button></div>
        {/if}
      {/if}
    </Popover.Content>
  </Popover.Portal>
</Popover.Root>
