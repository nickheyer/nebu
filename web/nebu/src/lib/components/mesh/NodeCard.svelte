<script lang="ts">
  import { UserX } from '@lucide/svelte';
  import { clock } from '$lib/state.svelte';
  import { nodeStateLabel, nodeTone, gbytes, tflops, shapeLabel, seatLabel } from '$lib/mesh';
  import { ago, count, plural, when } from '$lib/format';
  import { DeviceKind } from '$proto/host_pb';
  import type { Node } from '$proto/mesh_pb';
  import Card from '$lib/components/ui/Card.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Chip from '$lib/components/ui/Chip.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Disclosure from '$lib/components/ui/Disclosure.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import Devices from '$lib/components/Devices.svelte';

  let { n, acting, onforget }: { n: Node; acting: Set<string>; onforget: (n: Node) => void } = $props();

  const gpus = $derived(n.profile?.devices.filter((d) => d.kind !== DeviceKind.CPU).length ?? 0);
  const runtimes = $derived([...new Set(n.installs.map((i) => i.runtimeId))].sort());
</script>

<Card title={n.name || n.id.slice(0, 12)} meta={n.address || undefined}>
  {#snippet actions()}
    <State tone={nodeTone(n)} label={nodeStateLabel(n)} pulse={!n.self && n.state === 0} />
    {#if !n.self}<IconButton size="xs" icon={UserX} label="Forget node" loading={acting.has(n.id)} onclick={() => onforget(n)} />{/if}
  {/snippet}
  <div class="flex flex-col gap-4">
    <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-fg-muted">
      <span class="font-mono">{n.os}/{n.arch}</span>
      {#if n.version}<span>nebu {n.version}</span>{/if}
      <span>{plural(gpus, 'accelerator')}</span>
      <span>{plural(n.stored.length, 'model')}</span>
      {#if !n.self}<span title={when(n.seenAt)}>Seen {ago(n.seenAt, clock.now)}</span>{/if}
    </div>
    {#if n.profile}
      <Devices host={n.profile} />
    {:else}
      <p class="text-sm text-fg-muted">Waiting for device information</p>
    {/if}
    <div class="flex flex-wrap items-center gap-1.5">
      {#each runtimes as rt (rt)}<Chip text={rt} mono={false} />{/each}
    </div>
    <Disclosure label="Node details">
      <div class="flex flex-col gap-4">
        <Kv items={[
          ['Node ID', n.id],
          ['Address', n.address],
          ['Version', n.version]
        ]} />
        {#if n.capabilities.length}
          <div class="tbl-wrap">
            <table class="tbl dense">
              <thead><tr><th>Runtime</th><th>Distributions</th></tr></thead>
              <tbody>
                {#each n.capabilities as c (c.installId)}
                  <tr>
                    <td class="font-mono text-xs">{c.runtimeId} <span class="text-fg-muted">{c.version}</span></td>
                    <td class="text-xs text-fg-muted">{c.shapes.map(shapeLabel).join(', ')}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
        {#if n.routes.length || n.slots.length || n.formationIds.length || n.seats.length}
          <div class="flex flex-wrap items-center gap-1.5 text-xs text-fg-muted">
            {#if n.formationIds.length}<span>Coordinates {plural(n.formationIds.length, 'formation')}</span>{/if}
            {#each n.seats as s (s.instanceId || s.formationId + s.role + s.rank)}
              <Chip text={seatLabel(s.role, s.rank)} mono={false} title={`Formation ${s.formationId.slice(0, 8)}${s.transport ? ' · ' + s.transport : ''}`} />
            {/each}
            {#each n.slots as sl (sl.id)}<Chip text={sl.name} title={sl.formationId ? 'mesh slot' : 'slot'} />{/each}
            {#each n.routes as r (r.name)}<Chip text={r.name} title={`route on ${n.name}`} class="text-fg-faint" />{/each}
          </div>
        {/if}
        {#if n.throughput.length}
          <div class="tbl-wrap">
            <table class="tbl dense">
              <thead><tr><th>Device</th><th class="num">Bandwidth</th><th class="num">Compute</th><th class="num">Samples</th></tr></thead>
              <tbody>
                {#each n.throughput as t (t.deviceId)}
                  <tr>
                    <td class="font-mono text-xs" title={t.deviceId}>{n.profile?.devices.find((d) => d.id === t.deviceId)?.name || t.deviceId}</td>
                    <td class="num">{gbytes(t.streamBytesPerSecond)}</td>
                    <td class="num">{tflops(t.computeFlops)}</td>
                    <td class="num">{count(t.samples)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </div>
    </Disclosure>
  </div>
</Card>
