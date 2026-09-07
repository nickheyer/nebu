<script lang="ts">
  import { live, clock, probeHost, hostName, hostLabeled, homePath } from '$lib/state.svelte';
  import { ago, bytes, pct, storage } from '$lib/format';
  import { ProbeStatus } from '$proto/host_pb';
  import { RefreshCw, Server } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Meter from '$lib/components/ui/Meter.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import Devices from '$lib/components/Devices.svelte';

  let probing = $state(false);

  const host = $derived(live.host);
  const loading = $derived(!live.ready && !live.error && !host);

  // Probes the machine again and checks every dependency, as a task
  async function probe() {
    probing = true;
    await probeHost();
    probing = false;
  }
</script>

<PageHeader title={hostName() || 'Host'}>
  {#snippet meta()}
    {#if host}
      {#if hostLabeled()}<span class="font-mono">{host.hostname}</span>{/if}
      <span class="font-mono">{host.os}/{host.arch}</span>
      <span>probed {ago(host.probedAt, clock.now)}</span>
    {/if}
  {/snippet}
  <Button icon={RefreshCw} loading={probing} onclick={probe}>Probe host</Button>
</PageHeader>

<div class="flex flex-col gap-9">
  {#if loading}
    <div class="skeleton h-40" aria-busy="true"></div>
  {:else if !host}
    <Empty icon={Server} title="The host has not been probed yet">
      <Button size="sm" variant="primary" icon={RefreshCw} loading={probing} onclick={probe}>Probe</Button>
    </Empty>
  {:else}
    <Section title="Devices" count={host.devices.length || undefined}>
      <Devices {host} />
    </Section>

    <Section title="Storage">
      <table class="tbl">
        <thead><tr><th>Mount</th><th>Filesystem</th><th class="w-28">Use</th><th class="num">Free</th><th class="num">Total</th><th>Holds</th></tr></thead>
        <tbody>
          {#each host.storage as s (s.path)}
            {@const used = s.totalBytes > s.freeBytes ? s.totalBytes - s.freeBytes : 0n}
            <tr>
              <td class="font-mono text-xs">{s.path}</td>
              <td class="text-fg-muted">{s.filesystem}</td>
              <td><Meter value={used} max={s.totalBytes} auto /><div class="mt-1 text-xs tabular-nums text-fg-faint">{pct(used, s.totalBytes).toFixed(0)}%</div></td>
              <td class="num">{storage(s.freeBytes)}</td>
              <td class="num text-fg-muted">{storage(s.totalBytes)}</td>
              <td class="text-xs">
                <div class="flex flex-col gap-0.5">
                  {#each s.uses as u (u)}<span class="font-mono text-fg-muted" title={u}>{homePath(u)}</span>{/each}
                </div>
              </td>
            </tr>
          {:else}
            <tr><td colspan="6" class="text-fg-faint">No filesystems probed</td></tr>
          {/each}
        </tbody>
      </table>
    </Section>

    <div class="grid grid-cols-1 gap-9 xl:grid-cols-2">
      <Section title="Probes" count={host.probes.length || undefined}>
        <table class="tbl">
          <thead><tr><th>Probe</th><th>Status</th><th>Detail</th></tr></thead>
          <tbody>
            {#each host.probes as p (p.probeId)}
              <tr>
                <td class="font-mono text-xs">{p.probeId}</td>
                <td><State values={ProbeStatus} value={p.status} /></td>
                <td class="max-w-md truncate text-fg-muted" title={p.detail}>{p.detail || '–'}</td>
              </tr>
            {:else}
              <tr><td colspan="3" class="text-fg-faint">No probes ran</td></tr>
            {/each}
          </tbody>
        </table>
      </Section>

      <Section title="Facts" count={Object.keys(host.facts).length || undefined}>
        {#if Object.keys(host.facts).length}
          <Kv mono items={Object.entries(host.facts).sort(([a], [b]) => a.localeCompare(b))} />
        {:else}
          <p class="text-sm text-fg-faint">No facts probed.</p>
        {/if}
      </Section>
    </div>
  {/if}
</div>
