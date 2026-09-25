<script lang="ts">
  import { nodeName } from '$lib/state.svelte';
  import { chosen, shapeLabel, verdictLabel, verdictTone, seconds, tps, speedup } from '$lib/mesh';
  import type { FormationPlan } from '$proto/mesh_pb';
  import { Shape } from '$proto/estimate_pb';
  import { ChevronDown, ChevronRight } from '@lucide/svelte';
  import IconButton from './ui/IconButton.svelte';
  import State from './ui/State.svelte';

  let { plan, compact = false }: { plan: FormationPlan; compact?: boolean } = $props();

  const rows = $derived(plan.candidates);
  const selected = $derived(rows.findIndex((c) => chosen(c, plan.shape, plan.head)));
  let expanded = $state(-1);
  const rps = (n: number) => (n > 0 ? n.toFixed(2) : '–');
</script>

<p class="mb-3 text-xs text-fg-muted">{#if plan.referencePrompt}Estimates for {plan.referencePrompt.toLocaleString()} prompt and {plan.referenceCompletion.toLocaleString()} output tokens.{:else}Estimated performance{/if}</p>
<div class="tbl-wrap contain-inline-size">
  <table class="tbl {compact ? 'dense' : ''}">
    <thead>
      <tr><th>Distribution</th><th>Nodes</th><th>Fit</th><th class="num">First token</th><th class="num">Tokens/s</th>{#if !compact}<th class="num">Requests/s</th>{/if}<th class="num" title="Compared with the fastest single-node configuration">Speedup</th><th><span class="sr-only">Details</span></th></tr>
    </thead>
    <tbody>
      {#each rows as c, i (i)}
        {@const picked = i === selected}
        <tr class={picked ? 'row-active' : ''}>
          <td class="whitespace-nowrap font-medium text-fg">{c.shape === Shape.UNSPECIFIED ? '–' : shapeLabel(c.shape)}{#if picked}<div class="text-xs font-normal text-accent">Selected</div>{/if}</td>
          <td class="text-xs text-fg-muted">{#each c.nodeIds as nodeId (nodeId)}<div class="max-w-40 truncate" title={nodeName(nodeId)}>{nodeName(nodeId)}</div>{:else}–{/each}</td>
          <td><State tone={verdictTone(c.verdict)} label={verdictLabel(c.verdict)} /></td>
          <td class="num">{seconds(c.prefillSeconds)}</td>
          <td class="num">{tps(c.tokensPerSecond)}</td>
          {#if !compact}<td class="num">{rps(c.requestsPerSecond)}</td>{/if}
          <td class="num">{speedup(c.speedup)}</td>
          <td class="actions">
            {#if c.reason || (compact && c.requestsPerSecond > 0)}<IconButton size="xs" icon={expanded === i ? ChevronDown : ChevronRight} label="{expanded === i ? 'Hide' : 'Show'} {shapeLabel(c.shape)} details" aria-expanded={expanded === i} onclick={() => (expanded = expanded === i ? -1 : i)} />{/if}
          </td>
        </tr>
        {#if expanded === i}
          <tr>
            <td colspan={compact ? 7 : 8} class="bg-sunken/40">
              {#if compact && c.requestsPerSecond > 0}<p class="mb-1 text-xs text-fg-muted">Estimated requests/s: <span class="tabular-nums text-fg">{rps(c.requestsPerSecond)}</span></p>{/if}
              {#if c.reason}<p class="max-w-4xl text-sm leading-6 text-fg-muted wrap-anywhere">{c.reason}</p>{/if}
            </td>
          </tr>
        {/if}
      {:else}
        <tr><td colspan={compact ? 7 : 8} class="text-fg-faint">No configurations to compare</td></tr>
      {/each}
    </tbody>
  </table>
</div>
