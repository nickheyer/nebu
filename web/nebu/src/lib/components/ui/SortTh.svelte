<script lang="ts">
  import { ArrowDown, ArrowUp } from '@lucide/svelte';

  let { id, label, active = '', dir = 'desc', num = false, onSort, class: cls = '' }: { id: string; label: string; active?: string; dir?: 'asc' | 'desc'; num?: boolean; onSort: (id: string) => void; class?: string } = $props();
  const on = $derived(active === id);
</script>

<th class="{num ? 'num' : ''} {cls}" aria-sort={on ? (dir === 'asc' ? 'ascending' : 'descending') : 'none'}>
  <button type="button" class="inline-flex items-center gap-1 rounded px-0.5 uppercase hover:text-fg {on ? 'text-fg' : ''}" onclick={() => onSort(id)}>
    {label}
    {#if on}{#if dir === 'asc'}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}{/if}
  </button>
</th>
