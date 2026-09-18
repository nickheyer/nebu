<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Plus, X } from '@lucide/svelte';
  import TextInput from '../ui/TextInput.svelte';
  import Button from '../ui/Button.svelte';

  // Split pasted IDs on commas or whitespace. Keep text entries intact.
  let {
    items = $bindable([]),
    id,
    empty = '',
    mono = true,
    numeric = false,
    class: cls = '',
    children
  }: {
    items?: string[];
    id?: string;
    empty?: string;
    mono?: boolean;
    // Digits only, for shard IDs.
    numeric?: boolean;
    class?: string;
    children?: Snippet;
  } = $props();

  let draft = $state('');

  function add() {
    const parts = (mono ? draft.split(/[\s,]+/) : [draft]).map((s) => s.trim()).filter(Boolean);
    const next = [...items];
    for (const p of parts) {
      if (numeric && !/^\d+$/.test(p)) continue;
      if (!next.includes(p)) next.push(p);
    }
    items = next;
    draft = '';
  }

  function remove(i: number) {
    items = items.filter((_, j) => j !== i);
  }
</script>

<div class="flex flex-col gap-2 {cls}">
  {#if items.length}
    <div class="flex flex-wrap gap-1.5">
      {#each items as item, i (item)}
        <span class="inline-flex h-6 max-w-full items-center gap-1 rounded-sm border border-line bg-raised/40 pr-1 pl-2 text-xs text-fg {mono ? 'font-mono' : ''}">
          <span class="truncate">{item}</span>
          <button type="button" class="shrink-0 rounded-sm p-0.5 text-fg-faint transition-colors hover:text-fg" aria-label="Remove {item}" onclick={() => remove(i)}><X size={11} /></button>
        </span>
      {/each}
    </div>
  {/if}
  <div class="flex items-center gap-2">
    <TextInput
      {id}
      {mono}
      class="flex-1"
      bind:value={draft}
      {empty}
      inputmode={numeric ? 'numeric' : undefined}
      onkeydown={(e) => {
        if (e.key === 'Enter' || (mono && e.key === ',')) {
          e.preventDefault();
          add();
        }
      }}
      onblur={add}
    />
    <Button type="button" icon={Plus} aria-label="Add" onclick={add} disabled={!draft.trim()} />
    {#if children}{@render children()}{/if}
  </div>
</div>
