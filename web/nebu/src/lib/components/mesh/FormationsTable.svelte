<script lang="ts">
  import { goto } from '$app/navigation';
  import { Trash2 } from '@lucide/svelte';
  import { nodeName } from '$lib/state.svelte';
  import { shapeLabel, tps } from '$lib/mesh';
  import { FormationState, type Formation } from '$proto/mesh_pb';
  import Section from '$lib/components/ui/Section.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Chip from '$lib/components/ui/Chip.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';

  let { formations, acting, ondelete }: { formations: Formation[]; acting: Set<string>; ondelete: (f: Formation) => void } = $props();
</script>

<Section title="Formations" count={formations.length || undefined}>
  {#if formations.length === 0}
    <Empty compact title="No formations yet" description="Run a model and pick mesh nodes in the Run dialog.">
      <Button size="sm" href="/store">Browse models</Button>
    </Empty>
  {:else}
    <div class="tbl-wrap">
      <table class="tbl">
        <thead><tr><th>Name</th><th>Distribution</th><th>State</th><th>Coordinator</th><th>Nodes</th><th class="num">Est. tokens/s</th><th class="actions"></th></tr></thead>
        <tbody>
          {#each formations as f (f.id)}
            <tr class="row-link" onclick={() => goto(`/formations/${f.id}`)}>
              <td class="font-mono text-xs text-fg">
                <a href={`/formations/${f.id}`} class="link">{f.name}</a>
                {#if f.error}<p class="mt-1 max-w-sm truncate font-sans text-xs text-bad" title={f.error}>{f.error}</p>{/if}
              </td>
              <td>{shapeLabel(f.shape)}</td>
              <td><State values={FormationState} value={f.state} /></td>
              <td class="text-fg-muted">{f.conductorName || nodeName(f.conductor)}</td>
              <td>
                <div class="flex flex-wrap gap-1.5">
                  {#each [...new Set(f.seats.map((s) => s.nodeId))] as id (id)}
                    <Chip text={f.seats.find((s) => s.nodeId === id)?.nodeName || nodeName(id)} mono={false} />
                  {/each}
                </div>
              </td>
              <td class="num">{tps(f.plan?.tokensPerSecond)}</td>
              <td class="actions"><div class="flex justify-end"><IconButton size="xs" icon={Trash2} label="Delete" loading={acting.has(f.id)} onclick={(e) => { e.stopPropagation(); ondelete(f); }} /></div></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</Section>
