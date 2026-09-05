<script lang="ts">
  import { api } from '$lib/api';
  import { live, clock, probeHost } from '$lib/state.svelte';
  import { ago, bytes, enumLabel, pct, when } from '$lib/format';
  import { fail } from '$lib/toast.svelte';
  import { CheckStatus, DeviceKind, PoolKind, ProbeStatus, type DoctorReport } from '$proto/host_pb';
  import { RefreshCw, Stethoscope, Server, CircleCheck, CircleAlert, CircleX, ChevronDown } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Panel from '$lib/components/ui/Panel.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Badge from '$lib/components/ui/Badge.svelte';
  import StateBadge from '$lib/components/ui/StateBadge.svelte';
  import Meter from '$lib/components/ui/Meter.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import DeviceMeter from '$lib/components/DeviceMeter.svelte';

  let report = $state<DoctorReport | null>(null);
  let running = $state(false);
  let probing = $state(false);

  const host = $derived(live.host);
  const summary = $derived.by(() => {
    const c = report?.checks ?? [];
    return { ok: c.filter((x) => x.status === CheckStatus.OK).length, warn: c.filter((x) => x.status === CheckStatus.WARN).length, fail: c.filter((x) => x.status === CheckStatus.FAIL).length };
  });
  const checks = $derived([...(report?.checks ?? [])].sort((a, b) => order(b.status) - order(a.status)));

  function order(s: CheckStatus): number {
    return s === CheckStatus.FAIL ? 3 : s === CheckStatus.WARN ? 2 : s === CheckStatus.OK ? 1 : 0;
  }
  function poolOf(deviceId: string) {
    return host?.pools.find((p) => p.deviceId === deviceId && p.kind !== PoolKind.HOST);
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

<PageHeader title={host?.hostname || 'Host'} description="Everything nebu probed about this machine. Nothing here is configured, only observed.">
  {#snippet meta()}
    {#if host}
      <span class="font-mono">{host.os}/{host.arch}</span>
      <span>·</span>
      <span>probed {ago(host.probedAt, clock.now)}</span>
    {/if}
  {/snippet}
  <Button variant="outline" icon={RefreshCw} loading={probing} onclick={probe}>Probe again</Button>
  <Button variant="primary" icon={Stethoscope} loading={running} onclick={doctor}>Run doctor</Button>
</PageHeader>

<div class="flex flex-col gap-6">
  {#if report}
    <Panel title="Doctor" flush>
      {#snippet actions()}
        <span class="inline-flex items-center gap-1 text-xs text-ok"><CircleCheck size={13} /> {summary.ok}</span>
        <span class="inline-flex items-center gap-1 text-xs text-warn"><CircleAlert size={13} /> {summary.warn}</span>
        <span class="inline-flex items-center gap-1 text-xs text-bad"><CircleX size={13} /> {summary.fail}</span>
      {/snippet}
      <ul class="divide-y divide-line/60">
        {#each checks as c (c.id)}
          {@const Icon = checkIcon[c.status]}
          <li class="flex items-start gap-3 px-4 py-2.5">
            <Icon size={16} class="mt-0.5 shrink-0 {checkColor[c.status]}" />
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-center gap-2">
                <span class="font-mono text-xs text-fg-muted">{c.id}</span>
                <span class="text-sm text-fg">{c.summary}</span>
              </div>
              {#if c.hint}<div class="mt-0.5 text-xs leading-5 text-fg-faint">{c.hint}</div>{/if}
            </div>
          </li>
        {/each}
      </ul>
    </Panel>
  {/if}

  {#if !host}
    <div class="panel"><Empty icon={Server} title="No profile yet" description="Waiting for the daemon to answer with its host profile." /></div>
  {:else}
    <section>
      <h2 class="mb-3 text-sm font-semibold text-fg">Devices</h2>
      <div class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
        {#each host.devices as d (d.id)}
          <div class="flex flex-col gap-2">
            <DeviceMeter device={d} pool={poolOf(d.id)} />
            {#if Object.keys(d.facts).length}
              <details class="group rounded-lg border border-line bg-surface">
                <summary class="flex cursor-pointer items-center gap-2 px-3 py-2 text-xs text-fg-muted select-none">
                  <ChevronDown size={13} class="transition-transform group-open:rotate-180" />
                  {Object.keys(d.facts).length} facts
                </summary>
                <div class="border-t border-line px-3 py-2">
                  <Kv mono items={Object.entries(d.facts).sort(([a], [b]) => a.localeCompare(b))} />
                </div>
              </details>
            {/if}
          </div>
        {:else}
          <div class="panel md:col-span-2 xl:col-span-3"><Empty compact title="No devices" description="No probe emitted a device. Check the probes table below for what ran." /></div>
        {/each}
      </div>
    </section>

    <section class="grid grid-cols-1 gap-4 xl:grid-cols-2">
      <Panel title="Memory pools" description="where the planner can place bytes" flush>
        <table class="tbl">
          <thead><tr><th>pool</th><th>kind</th><th>device</th><th class="w-40">use</th><th class="num">free</th><th class="num">total</th></tr></thead>
          <tbody>
            {#each host.pools as p (p.id)}
              {@const used = p.totalBytes > p.freeBytes ? p.totalBytes - p.freeBytes : 0n}
              <tr>
                <td class="max-w-[11rem] truncate font-mono text-xs" title={p.id}>{p.id}</td>
                <td><Badge size="xs" label={enumLabel(PoolKind, p.kind)} tone={p.kind === PoolKind.HOST ? 'neutral' : 'accent'} /></td>
                <td class="max-w-[11rem] truncate font-mono text-xs text-fg-muted" title={p.deviceId}>{p.deviceId || '–'}</td>
                <td><Meter value={used} max={p.totalBytes} /></td>
                <td class="num text-xs">{bytes(p.freeBytes)}</td>
                <td class="num text-xs">{bytes(p.totalBytes)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </Panel>

      <Panel title="Storage" description="filesystems holding nebu data" flush>
        <table class="tbl">
          <thead><tr><th>mount</th><th>fs</th><th class="w-32">use</th><th class="num">free</th><th>holds</th></tr></thead>
          <tbody>
            {#each host.storage as s (s.path)}
              {@const used = s.totalBytes > s.freeBytes ? s.totalBytes - s.freeBytes : 0n}
              <tr>
                <td class="font-mono text-xs">{s.path}</td>
                <td class="text-xs">{s.filesystem}</td>
                <td><Meter value={used} max={s.totalBytes} /><div class="mt-0.5 text-[10.5px] text-fg-faint tabular-nums">{pct(used, s.totalBytes).toFixed(0)}% used</div></td>
                <td class="num text-xs">{bytes(s.freeBytes)}</td>
                <td class="max-w-[16rem] truncate text-xs text-fg-muted" title={s.uses.join('\n')}>{s.uses.map((u) => u.split('/').slice(-2).join('/')).join(', ')}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </Panel>
    </section>

    <section class="grid grid-cols-1 gap-4 xl:grid-cols-2">
      <Panel title="Probes" description="vendor tools and files read to build this profile" flush>
        <table class="tbl">
          <thead><tr><th>probe</th><th>status</th><th>detail</th></tr></thead>
          <tbody>
            {#each host.probes as p (p.probeId)}
              <tr>
                <td class="font-mono text-xs">{p.probeId}</td>
                <td><StateBadge values={ProbeStatus} value={p.status} size="xs" /></td>
                <td class="max-w-md truncate text-xs text-fg-muted" title={p.detail}>{p.detail || '–'}</td>
              </tr>
            {:else}
              <tr><td colspan="3" class="text-xs text-fg-faint">No probes recorded.</td></tr>
            {/each}
          </tbody>
        </table>
      </Panel>

      <Panel title="Host facts" description="values spec templates and constraints can reference">
        {#if Object.keys(host.facts).length}
          <Kv mono items={Object.entries(host.facts).sort(([a], [b]) => a.localeCompare(b))} />
        {:else}
          <p class="text-sm text-fg-faint">No host level facts were emitted.</p>
        {/if}
        <p class="mt-3 text-[11px] text-fg-faint">Profile from {when(host.probedAt)}</p>
      </Panel>
    </section>
  {/if}
</div>
