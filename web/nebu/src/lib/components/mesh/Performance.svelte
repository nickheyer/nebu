<script lang="ts">
  import { Plus, Trash2 } from '@lucide/svelte';
  import { clock } from '$lib/state.svelte';
  import { classLabel, gbytes, tflops, shapeLabel } from '$lib/mesh';
  import { ago, count, when } from '$lib/format';
  import type { DeviceProfile, FormationRatio } from '$proto/mesh_pb';
  import Section from '$lib/components/ui/Section.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';

  let {
    profiles,
    profileUse,
    ratios,
    ondelete,
    onadd
  }: {
    // Rows in use by this mesh first
    profiles: DeviceProfile[];
    // The mesh devices each pattern prices
    profileUse: Map<string, { key: string; label: string }[]>;
    ratios: FormationRatio[];
    ondelete: (p: DeviceProfile) => void;
    onadd: () => void;
  } = $props();
</script>

<Section title="Device profiles" count={profiles.length || undefined}>
  {#snippet actions()}
    <Button size="sm" icon={Plus} onclick={onadd}>Add profile</Button>
  {/snippet}
  <p class="text-sm text-fg-muted">Planning numbers for a device until measured requests replace them.</p>
  <div class="tbl-wrap">
    <table class="tbl dense">
      <thead><tr><th>Device pattern</th><th>Prices</th><th class="num">Memory bandwidth</th><th class="num">Compute</th><th>Source</th><th class="actions"></th></tr></thead>
      <tbody>
        {#each profiles as p (p.pattern)}
          <tr class={profileUse.has(p.pattern) ? 'row-active' : ''}>
            <td class="font-mono text-xs text-fg">{p.pattern}</td>
            <td class="text-xs text-fg-muted">{#each profileUse.get(p.pattern) ?? [] as d (d.key)}<div>{d.label}</div>{:else}-{/each}</td>
            <td class="num">{gbytes(p.streamBytesPerSecond)}</td>
            <td class="num">{tflops(p.computeFlops)}</td>
            <td class="text-fg-muted">{p.builtin ? 'Default' : 'Custom'}{#if !p.builtin && p.updatedAt}<span class="ml-2 text-xs" title={when(p.updatedAt)}>{ago(p.updatedAt, clock.now)}</span>{/if}</td>
            <td class="actions"><div class="flex justify-end">{#if !p.builtin}<IconButton size="xs" icon={Trash2} label="Remove" onclick={() => ondelete(p)} />{/if}</div></td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</Section>
{#if ratios.length}
  <Section title="Measured adjustments" count={ratios.length}>
    <p class="text-sm text-fg-muted">Multipliers on estimated response times, from completed requests.</p>
    <div class="tbl-wrap">
      <table class="tbl dense">
        <thead><tr><th>Distribution</th><th>Runtime</th><th>Link class</th><th class="num">First token</th><th class="num">Per token</th><th class="num">Requests</th></tr></thead>
        <tbody>
          {#each ratios as r (r.shape + r.runtimeId + r.linkClass)}
            <tr>
              <td>{shapeLabel(r.shape)}</td>
              <td class="font-mono text-xs">{r.runtimeId}</td>
              <td>{classLabel(r.linkClass)}</td>
              <td class="num">{r.ttftRatio.toFixed(2)}×</td>
              <td class="num">{r.tptRatio.toFixed(2)}×</td>
              <td class="num">{count(r.samples)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </Section>
{/if}
