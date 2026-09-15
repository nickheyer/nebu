<script lang="ts">
  import { ParamType, type Param } from '$proto/runtime_pb';
  import { enumLabel } from '$lib/format';
  import Empty from '$lib/components/ui/Empty.svelte';

  let { params }: { params: Param[] } = $props();

  const groups = $derived.by(() => {
    const out: { name: string; params: Param[] }[] = [];
    for (const p of params) {
      let g = out.find((x) => x.name === p.group);
      if (!g) out.push((g = { name: p.group, params: [] }));
      g.params.push(p);
    }
    return out;
  });

  const range = (p: Param) => [p.min || p.max ? (p.max ? `${p.min} – ${p.max}` : `≥ ${p.min}`) : '', p.step ? `step ${p.step}` : ''].filter(Boolean).join(' · ');
</script>

{#if !params.length}
  <Empty compact title="No parameters" />
{:else}
  <div class="overflow-x-auto">
    <table class="tbl">
      <thead><tr><th>Parameter</th><th>Flag</th><th>Type</th><th>Default</th><th>Range</th><th>Choices</th></tr></thead>
      <tbody>
        {#each groups as g (g.name)}
          {#if g.name}<tr><td colspan="6" class="caps !pt-4 text-fg-faint">{g.name}</td></tr>{/if}
          {#each g.params as p (p.name)}
            <tr>
              <td title={p.description}>
                <div class="flex items-center gap-2 text-fg">
                  {p.label || p.name}
                  {#if p.advanced}<span class="caps rounded-sm border border-line px-1 text-[10px] text-fg-faint">advanced</span>{/if}
                </div>
                <div class="font-mono text-xs text-fg-faint">{p.name}</div>
              </td>
              <td class="font-mono text-xs text-fg-muted">{p.flag || p.env || '–'}</td>
              <td class="text-fg-muted">{enumLabel(ParamType, p.type)}</td>
              <td>
                <span class="font-mono text-xs text-fg-muted">{p.default || '–'}</span>
                {#if p.solved && p.rule}<div class="max-w-xs truncate text-xs text-fg-faint" title={p.rule}>{p.rule}</div>{/if}
              </td>
              <td class="text-xs tabular-nums whitespace-nowrap text-fg-muted">{range(p)}{#if p.unit}<span class="ml-1 text-fg-faint">{p.unit}</span>{/if}</td>
              <td class="max-w-xs truncate font-mono text-xs text-fg-muted" title={p.choices.join(' ')}>{p.choices.join(' ')}</td>
            </tr>
          {/each}
        {/each}
      </tbody>
    </table>
  </div>
{/if}
