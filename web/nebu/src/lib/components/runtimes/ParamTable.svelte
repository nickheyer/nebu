<script lang="ts">
  import { accepts, defaultText, paramGroups } from '$lib/runtimes';
  import type { Param } from '$proto/runtime_pb';

  let { params }: { params: Param[] } = $props();

  const groups = $derived(paramGroups(params));
</script>

<div class="overflow-x-auto">
  <table class="tbl">
    <thead>
      <tr>
        <th>Parameter</th>
        <th>Flag</th>
        <th>When unset</th>
        <th>Accepts</th>
      </tr>
    </thead>
    <tbody>
      {#each groups as g (g.name)}
        {#if g.name}
          <tr><td colspan="4" class="caps pt-5! text-fg-faint">{g.name}</td></tr>
        {/if}
        {#each g.params as p (p.name)}
          <tr>
            <td class="max-w-md">
              <div class="flex flex-wrap items-baseline gap-x-2">
                <span class="text-fg">{p.label || p.name}</span>
                <span class="font-mono text-xs text-fg-faint">{p.name}</span>
                {#if p.advanced}<span class="text-xs text-fg-faint">advanced</span>{/if}
              </div>
              {#if p.description}<div class="text-xs leading-5 text-fg-muted">{p.description}</div>{/if}
            </td>
            <td class="font-mono text-xs whitespace-nowrap text-fg-muted">{p.flag || p.env || '–'}</td>
            <td class="max-w-xs">
              <span class="font-mono text-xs {p.solved ? 'text-accent' : 'text-fg'}">{defaultText(p) || '–'}</span>
              {#if p.solved}<div class="text-xs leading-5 text-fg-muted">{p.rule}</div>{/if}
            </td>
            <td class="max-w-xs text-xs text-fg-muted tabular-nums">{accepts(p)}</td>
          </tr>
        {/each}
      {/each}
    </tbody>
  </table>
</div>
