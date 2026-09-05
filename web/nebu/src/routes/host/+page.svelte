<script lang="ts">
  import { api } from '$lib/api';
  import { live, clock, probeHost, hostName, hostLabeled } from '$lib/state.svelte';
  import { ago, bytes, enumLabel, pct, plural, tail } from '$lib/format';
  import { fail } from '$lib/toast.svelte';
  import { CheckStatus, PoolKind, ProbeStatus, type DoctorReport } from '$proto/host_pb';
  import { RefreshCw, Stethoscope, Server, CircleCheck, CircleAlert, CircleX } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Meter from '$lib/components/ui/Meter.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import Disclosure from '$lib/components/ui/Disclosure.svelte';
  import MemoryRows from '$lib/components/MemoryRows.svelte';

  let report = $state<DoctorReport | null>(null);
  let running = $state(false);
  let probing = $state(false);
  let showOk = $state(false);

  const host = $derived(live.host);
  const loading = $derived(!live.ready && !live.error && !host);
  const checks = $derived([...(report?.checks ?? [])].sort((a, b) => order(b.status) - order(a.status)));
  const attention = $derived(checks.filter((c) => c.status !== CheckStatus.OK));
  const passing = $derived(checks.filter((c) => c.status === CheckStatus.OK));
  const fails = $derived(checks.filter((c) => c.status === CheckStatus.FAIL).length);
  const warns = $derived(checks.filter((c) => c.status === CheckStatus.WARN).length);

  function order(s: CheckStatus): number {
    return s === CheckStatus.FAIL ? 3 : s === CheckStatus.WARN ? 2 : s === CheckStatus.OK ? 1 : 0;
  }
  async function doctor() {
    running = true;
    try {
      report = (await api.host.doctor({})).report ?? null;
      if (report?.profile) live.host = report.profile;
    } catch (err) {
      fail(err, 'Doctor failed');
    } finally {
      running = false;
    }
  }
  async function probe() {
    probing = true;
    await probeHost();
    probing = false;
  }

  const checkIcon = { [CheckStatus.OK]: CircleCheck, [CheckStatus.WARN]: CircleAlert, [CheckStatus.FAIL]: CircleX, [CheckStatus.UNSPECIFIED]: CircleAlert };
  const checkColor = { [CheckStatus.OK]: 'text-ok', [CheckStatus.WARN]: 'text-warn', [CheckStatus.FAIL]: 'text-bad', [CheckStatus.UNSPECIFIED]: 'text-fg-faint' };
</script>

{#snippet checkList(list: typeof checks)}
  <ul class="divide-y divide-line/70 border-y border-line">
    {#each list as c (c.id)}
      {@const Icon = checkIcon[c.status]}
      <li class="flex items-start gap-3 px-1.5 py-2.5">
        <Icon size={16} class="mt-0.5 shrink-0 {checkColor[c.status]}" />
        <div class="min-w-0 flex-1">
          <div class="flex flex-wrap items-baseline gap-x-3">
            <span class="text-sm text-fg">{c.summary}</span>
            <span class="font-mono text-xs text-fg-faint">{c.id}</span>
          </div>
          {#if c.hint}<div class="mt-0.5 font-mono text-xs leading-5 text-fg-muted">{c.hint}</div>{/if}
        </div>
      </li>
    {/each}
  </ul>
{/snippet}

<PageHeader title={hostName() || 'Host'}>
  {#snippet meta()}
    {#if host}
      {#if hostLabeled()}<span class="font-mono">{host.hostname}</span>{/if}
      <span class="font-mono">{host.os}/{host.arch}</span>
      <span>probed {ago(host.probedAt, clock.now)}</span>
    {/if}
  {/snippet}
  <IconButton icon={RefreshCw} label="Probe host" variant="secondary" loading={probing} onclick={probe} />
  <Button variant="primary" icon={Stethoscope} loading={running} onclick={doctor}>Doctor</Button>
</PageHeader>

<div class="flex flex-col gap-9">
  {#if report}
    <Section title="Doctor" meta={[fails ? plural(fails, 'failure') : '', warns ? plural(warns, 'warning') : '', plural(passing.length, 'ok', 'ok')].filter(Boolean).join(' · ')}>
      {#if attention.length}
        {@render checkList(attention)}
      {/if}
      <Disclosure label="Passing" summary={String(passing.length)} bind:open={showOk}>
        {@render checkList(passing)}
      </Disclosure>
    </Section>
  {/if}

  {#if loading}
    <div class="skeleton h-40" aria-busy="true"></div>
  {:else if !host}
    <Empty icon={Server} title="No profile yet">
      <Button size="sm" variant="primary" icon={RefreshCw} loading={probing} onclick={probe}>Probe</Button>
    </Empty>
  {:else}
    <Section title="Devices" count={host.devices.length || undefined}>
      <MemoryRows {host} />
    </Section>

    <div class="grid grid-cols-1 gap-9 xl:grid-cols-2">
      <Section title="Memory pools" info="Where the planner places weights and cache">
        <table class="tbl">
          <thead><tr><th>Pool</th><th>Kind</th><th class="w-32">Use</th><th class="num">Free</th><th class="num">Total</th></tr></thead>
          <tbody>
            {#each host.pools as p (p.id)}
              {@const used = p.totalBytes > p.freeBytes ? p.totalBytes - p.freeBytes : 0n}
              <tr>
                <td class="max-w-[12rem] truncate font-mono text-xs" title={p.id}>{p.id}</td>
                <td class="text-fg-muted">{enumLabel(PoolKind, p.kind)}</td>
                <td><Meter value={used} max={p.totalBytes} /></td>
                <td class="num">{bytes(p.freeBytes)}</td>
                <td class="num text-fg-muted">{bytes(p.totalBytes)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </Section>

      <Section title="Storage">
        <table class="tbl">
          <thead><tr><th>Mount</th><th>Filesystem</th><th class="w-28">Use</th><th class="num">Free</th><th>Holds</th></tr></thead>
          <tbody>
            {#each host.storage as s (s.path)}
              {@const used = s.totalBytes > s.freeBytes ? s.totalBytes - s.freeBytes : 0n}
              <tr>
                <td class="font-mono text-xs">{s.path}</td>
                <td class="text-fg-muted">{s.filesystem}</td>
                <td><Meter value={used} max={s.totalBytes} auto /><div class="mt-1 text-xs tabular-nums text-fg-faint">{pct(used, s.totalBytes).toFixed(0)}%</div></td>
                <td class="num">{bytes(s.freeBytes)}</td>
                <td class="max-w-[16rem] truncate text-xs text-fg-muted" title={s.uses.join('\n')}>{s.uses.map(tail).join(' · ')}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </Section>
    </div>

    <div class="grid grid-cols-1 gap-9 xl:grid-cols-2">
      <Section title="Probes" info="Vendor tools and files read to build this profile">
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
              <tr><td colspan="3" class="text-fg-faint">No probes</td></tr>
            {/each}
          </tbody>
        </table>
      </Section>

      <Section title="Facts" info="Values spec templates and constraints reference">
        {#if Object.keys(host.facts).length}
          <Kv mono items={Object.entries(host.facts).sort(([a], [b]) => a.localeCompare(b))} />
        {:else}
          <p class="text-sm text-fg-faint">None</p>
        {/if}
      </Section>
    </div>
  {/if}
</div>
