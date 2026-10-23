<script lang="ts">
  import { Radar } from '@lucide/svelte';
  import { classLabel, classTone, gbits, micros } from '$lib/mesh';
  import type { Node, Link } from '$proto/mesh_pb';
  import Section from '$lib/components/ui/Section.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Disclosure from '$lib/components/ui/Disclosure.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';

  let { nodes, links, probing, onprobe }: { nodes: Node[]; links: Link[]; probing: boolean; onprobe: () => void } = $props();

  const alone = $derived(nodes.filter((n) => !n.self).length === 0);
</script>

<Section title="Links" count={links.length || undefined}>
  {#snippet actions()}
    <Button size="sm" icon={Radar} loading={probing} disabled={alone} onclick={onprobe}>Measure links</Button>
  {/snippet}
  {#if alone}
    <Empty compact title="Add another node to measure links" />
  {:else}
    <div class="tbl-wrap">
      <table class="tbl dense">
        <thead>
          <tr><th>From / To</th>{#each nodes as n (n.id)}<th>{n.name || n.id.slice(0, 8)}</th>{/each}</tr>
        </thead>
        <tbody>
          {#each nodes as from (from.id)}
            <tr>
              <td class="font-medium text-fg">{from.name || from.id.slice(0, 8)}</td>
              {#each nodes as to (to.id)}
                {@const l = from.id === to.id ? undefined : links.find((x) => x.from === from.id && x.to === to.id)}
                <td>
                  {#if from.id === to.id}
                    <span class="text-fg-faint">–</span>
                  {:else if l}
                    <div class="flex flex-col gap-0.5" title={l.detail}>
                      <State tone={classTone(l.class)} label={classLabel(l.class)} />
                      <span class="whitespace-nowrap text-xs tabular-nums text-fg-muted">{micros(l.rttUs)} · {gbits(l.streamBytesPerSecond)}</span>
                      <span class="text-xs text-fg-muted">{l.interface}{#if l.bandwidthHeld} · bandwidth from the last run{/if}</span>
                      {#if l.detail}<span class="max-w-xs text-xs leading-5 text-fg-muted wrap-anywhere">{l.detail}</span>{/if}
                      <Disclosure label="Details">
                        <Kv items={[
                          ['Round trip', micros(l.rttUs)],
                          ['Single stream', gbits(l.streamBytesPerSecond)],
                          ['Four streams', gbits(l.aggregateBytesPerSecond)],
                          ['Interface speed', gbits(Number(l.interfaceBitsPerSecond) / 8)],
                          ['MTU', l.mtu || undefined],
                          ['RDMA device', l.rdmaDevice || undefined],
                          ['Subnet', l.subnet ? 'Same subnet' : 'Routed']
                        ]} omitEmpty />
                      </Disclosure>
                    </div>
                  {:else}
                    <span class="text-xs text-fg-faint">unmeasured</span>
                  {/if}
                </td>
              {/each}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</Section>
