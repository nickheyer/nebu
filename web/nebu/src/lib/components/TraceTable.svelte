<script lang="ts">
  import { clock } from '$lib/state.svelte';
  import { ago, enumLabel, millisBetween, ms, rate, when } from '$lib/format';
  import { TraceKind, type Trace } from '$proto/gateway_pb';
  import { ApiFlavor } from '$proto/runtime_pb';
  import Empty from './ui/Empty.svelte';

  // Requests through the gateway, one row each, the newest first
  let { traces, selected = '', onSelect, showRoute = true, compact = false }: { traces: Trace[]; selected?: string; onSelect?: (t: Trace) => void; showRoute?: boolean; compact?: boolean } = $props();

  const flavor = (f: ApiFlavor) => enumLabel(ApiFlavor, f).replace('unspecified', 'openai');
  const format = (t: Trace) => (t.translated ? `${flavor(t.clientApi)} → ${flavor(t.upstreamApi)}` : flavor(t.clientApi));
  const statusTone = (t: Trace) => (!t.finishedAt ? 'text-accent' : t.status >= 500 ? 'text-bad' : t.status >= 400 ? 'text-warn' : 'text-ok');
  const outRate = (t: Trace) => rate(t.completionTokens, millisBetween(t.firstTokenAt, t.finishedAt));
</script>

{#if traces.length === 0}
  <Empty compact title="No requests yet" />
{:else}
  <div class="overflow-x-auto">
    <table class="tbl">
      <thead>
        <tr>
          <th>Time</th>
          {#if showRoute}<th>Model</th>{/if}
          <th>Kind</th>
          <th>Format</th>
          <th>Status</th>
          <th class="num">First token</th>
          <th class="num">Total</th>
          <th class="num">In</th>
          <th class="num">Out</th>
          {#if !compact}<th class="num">Speed</th><th>Stop</th>{/if}
        </tr>
      </thead>
      <tbody>
        {#each traces as t (t.id)}
          <tr class="row-link {selected === t.id ? 'row-active' : ''}" onclick={() => onSelect?.(t)}>
            <td class="whitespace-nowrap text-fg-muted" title={when(t.startedAt)}>{ago(t.startedAt, clock.now)}</td>
            {#if showRoute}<td class="max-w-[12rem] truncate font-mono text-xs text-fg">{t.route || '–'}</td>{/if}
            <td class="text-fg-muted">{enumLabel(TraceKind, t.kind)}</td>
            <td class="font-mono text-xs text-fg-muted">{format(t)}</td>
            <td class="whitespace-nowrap tabular-nums {statusTone(t)}">{t.finishedAt ? t.status : 'live'}{#if t.error}<span class="ml-2 inline-block {compact ? 'max-w-[7rem]' : 'max-w-[14rem]'} truncate align-bottom text-xs text-bad" title={t.error}>{t.error}</span>{/if}</td>
            <td class="num text-fg-muted">{ms(millisBetween(t.startedAt, t.firstTokenAt))}</td>
            <td class="num">{ms(millisBetween(t.startedAt, t.finishedAt))}</td>
            <td class="num text-fg-muted">{t.promptTokens || '–'}</td>
            <td class="num text-fg-muted">{t.completionTokens || '–'}</td>
            {#if !compact}
              <td class="num text-fg-muted">{outRate(t)}</td>
              <td class="text-fg-muted">{t.stop || '–'}</td>
            {/if}
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}
