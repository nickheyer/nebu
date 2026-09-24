<script lang="ts">
  import { MediaQuery } from 'svelte/reactivity';
  import { live, clock, probeHost, hostName, hostLabeled } from '$lib/state.svelte';
  import { ago } from '$lib/format';
  import { ProbeStatus } from '$proto/host_pb';
  import { RefreshCw, Server } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Tabs from '$lib/components/ui/Tabs.svelte';
  import FactTable from '$lib/components/ui/FactTable.svelte';
  import Devices from '$lib/components/Devices.svelte';
  import Storage from '$lib/components/Storage.svelte';
  import DaemonLog from '$lib/components/DaemonLog.svelte';

  let probing = $state(false);
  const split = new MediaQuery('(min-width: 96rem)');
  let pane = $state('host');

  const host = $derived(live.host);
  const loading = $derived(!live.ready && !live.error && !host);

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

{#if !split.current}
  <Tabs
    class="mb-6"
    bind:value={pane}
    tabs={[
      { id: 'host', label: 'Host' },
      { id: 'log', label: 'Log' }
    ]}
  />
{/if}

<div class={split.current ? 'grid grid-cols-2 gap-8' : ''}>
  {#if split.current || pane === 'host'}
    <div class="flex min-w-0 flex-col gap-10">
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

        <Section title="Storage" count={host.storage.length || undefined}>
          <Storage {host} />
        </Section>

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
          <FactTable facts={host.facts} />
        </Section>
      {/if}
    </div>
  {/if}

  {#if split.current}
    <Section title="Log" class="sticky top-7 min-w-0 self-start">
      <DaemonLog height="h-[calc(100vh-9rem)]" />
    </Section>
  {:else if pane === 'log'}
    <DaemonLog height="h-[calc(100vh-17rem)] min-h-64 lg:h-[calc(100vh-13.5rem)]" />
  {/if}
</div>
