<script lang="ts">
  import { api } from '$lib/api';
  import { live, clock, probeHost, hostName } from '$lib/state.svelte';
  import { ago, bytes, enumLabel, pct, when } from '$lib/format';
  import { fail } from '$lib/toast.svelte';
  import { CheckStatus, PoolKind, ProbeStatus, type DoctorReport } from '$proto/host_pb';
  import { RefreshCw, Stethoscope, Server, CircleCheck, CircleAlert, CircleX } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Pill from '$lib/components/ui/Pill.svelte';
  import StatePill from '$lib/components/ui/StatePill.svelte';
  import Meter from '$lib/components/ui/Meter.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import MemoryRows from '$lib/components/MemoryRows.svelte';

  let report = $state<DoctorReport | null>(null);
  let running = $state(false);
  let probing = $state(false);

  const host = $derived(live.host);
  const loading = $derived(!live.ready && !live.error && !host);
  const summary = $derived.by(() => {
    const c = report?.checks ?? [];
    return { ok: c.filter((x) => x.status === CheckStatus.OK).length, warn: c.filter((x) => x.status === CheckStatus.WARN).length, fail: c.filter((x) => x.status === CheckStatus.FAIL).length };
  });
  const checks = $derived([...(report?.checks ?? [])].sort((a, b) => order(b.status) - order(a.status)));

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

<PageHeader title={hostName() || 'Host'}>
  {#snippet meta()}
    {#if host}
      {#if live.settings?.hostLabel && live.settings.hostLabel !== host.hostname}<span class="font-mono">{host.hostname}</span>{/if}
      <span class="font-mono">{host.os}/{host.arch}</span>
      <span>probed {ago(host.probedAt, clock.now)}</span>
    {/if}
  {/snippet}
  <Button icon={RefreshCw} loading={probing} onclick={probe}>Probe again</Button>
  <Button variant="primary" icon={Stethoscope} loading={running} onclick={doctor}>Run doctor</Button>
</PageHeader>

<div class="flex flex-col gap-6">
  {#if report}
    <Card title="Doctor" description="Every health check, worst first" flush>
      {#snippet actions()}
        {#if summary.fail}<Pill tone="bad" label="{summary.fail} failing" />{/if}
        {#if summary.warn}<Pill tone="warn" label="{summary.warn} warnings" />{/if}
        <Pill tone="ok" label="{summary.ok} ok" />
      {/snippet}
      <ul class="divide-y divide-line/60">
        {#each checks as c (c.id)}
          {@const Icon = checkIcon[c.status]}
          <li class="flex items-start gap-3 px-5 py-3">
            <Icon size={18} class="mt-0.5 shrink-0 {checkColor[c.status]}" />
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-baseline gap-x-3">
                <span class="text-sm text-fg">{c.summary}</span>
                <span class="font-mono text-xs text-fg-faint">{c.id}</span>
              </div>
              {#if c.hint}<div class="mt-0.5 text-sm leading-6 text-fg-muted">{c.hint}</div>{/if}
            </div>
          </li>
        {/each}
      </ul>
    </Card>
  {/if}

  {#if loading}
    <div class="card h-40" aria-busy="true"></div>
  {:else if !host}
    <div class="card"><Empty icon={Server} title="No profile yet" description="Probe the host to read its devices and memory." /></div>
  {:else}
    <section>
      <h2 class="section-title mb-3">Memory</h2>
      <MemoryRows {host} cpu facts />
    </section>

    <section class="grid grid-cols-1 gap-6 xl:grid-cols-2">
      <Card title="Memory pools" description="Where the planner places weights and cache" flush>
        <div class="overflow-x-auto">
          <table class="tbl">
            <thead><tr><th>Pool</th><th>Kind</th><th class="w-40">Use</th><th class="num">Free</th><th class="num">Total</th></tr></thead>
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
        </div>
      </Card>

      <Card title="Storage" flush>
        <div class="overflow-x-auto">
          <table class="tbl">
            <thead><tr><th>Mount</th><th>Filesystem</th><th class="w-32">Use</th><th class="num">Free</th><th>Holds</th></tr></thead>
            <tbody>
              {#each host.storage as s (s.path)}
                {@const used = s.totalBytes > s.freeBytes ? s.totalBytes - s.freeBytes : 0n}
                <tr>
                  <td class="font-mono text-xs">{s.path}</td>
                  <td class="text-fg-muted">{s.filesystem}</td>
                  <td><Meter value={used} max={s.totalBytes} /><div class="mt-1 text-xs tabular-nums text-fg-faint">{pct(used, s.totalBytes).toFixed(0)}%</div></td>
                  <td class="num">{bytes(s.freeBytes)}</td>
                  <td class="max-w-[16rem] truncate text-fg-muted" title={s.uses.join('\n')}>{s.uses.map((u) => u.split('/').slice(-2).join('/')).join(' · ')}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </Card>
    </section>

    <section class="grid grid-cols-1 gap-6 xl:grid-cols-2">
      <Card title="Probes" description="Vendor tools and files read to build this profile" flush>
        <div class="overflow-x-auto">
          <table class="tbl">
            <thead><tr><th>Probe</th><th>Status</th><th>Detail</th></tr></thead>
            <tbody>
              {#each host.probes as p (p.probeId)}
                <tr>
                  <td class="font-mono text-xs">{p.probeId}</td>
                  <td><StatePill values={ProbeStatus} value={p.status} /></td>
                  <td class="max-w-md truncate text-fg-muted" title={p.detail}>{p.detail || '–'}</td>
                </tr>
              {:else}
                <tr><td colspan="3" class="text-fg-faint">No probes</td></tr>
              {/each}
            </tbody>
          </table>
        </div>
      </Card>

      <Card title="Host facts" description="Values spec templates and constraints reference">
        {#if Object.keys(host.facts).length}
          <Kv mono items={Object.entries(host.facts).sort(([a], [b]) => a.localeCompare(b))} />
        {:else}
          <p class="text-sm text-fg-faint">None</p>
        {/if}
      </Card>
    </section>
  {/if}
</div>
