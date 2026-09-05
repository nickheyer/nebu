<script lang="ts">
  import { ArrowDown, ArrowUp } from '@lucide/svelte';

  let { id, label, active = '', dir = 'desc', num = false, onSort }: { id: string; label: string; active?: string; dir?: 'asc' | 'desc'; num?: boolean; onSort: (id: string) => void } = $props();
  const on = $derived(active === id);
</script>

<th class={num ? 'num' : ''} aria-sort={on ? (dir === 'asc' ? 'ascending' : 'descending') : 'none'}>
  <button type="button" class="inline-flex items-center gap-1 rounded hover:text-fg {on ? 'text-fg' : ''}" onclick={() => onSort(id)}>
    {label}
    {#if on}{#if dir === 'asc'}<ArrowUp size={12} />{:else}<ArrowDown size={12} />{/if}{/if}
  </button>
</th>
