<script lang="ts">
  type Col = string | { w: string; num?: boolean; sub?: boolean };
  let { rows = 4, cols }: { rows?: number; cols: Col[] } = $props();
  const spec = $derived(cols.map((c) => (typeof c === 'string' ? { w: c, num: false, sub: false } : { num: false, sub: false, ...c })));
</script>

{#each Array(rows) as _, i (i)}
  <tr aria-busy="true">
    {#each spec as c, j (j)}
      <td class={c.num ? 'num' : ''}>
        <div class="skeleton h-3 {c.w} {c.num ? 'ml-auto' : ''}" style={j === 0 ? `width: ${55 + ((i * 17) % 35)}%` : ''}></div>
        {#if c.sub}<div class="skeleton mt-2 h-2.5 w-2/5"></div>{/if}
      </td>
    {/each}
  </tr>
{/each}
