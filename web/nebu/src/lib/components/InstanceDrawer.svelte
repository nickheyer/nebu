<script lang="ts">
  import { api } from '$lib/api';
  import { live, clock, instanceLive, slotName } from '$lib/state.svelte';
  import { launch } from '$lib/launch';
  import { bytes, when, duration, enumLabel } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { InstanceState } from '$proto/instance_pb';
  import { Square, RotateCcw, Wrench, ExternalLink } from '@lucide/svelte';
  import Drawer from './ui/Drawer.svelte';
  import Tabs from './ui/Tabs.svelte';
  import Kv from './ui/Kv.svelte';
  import Button from './ui/Button.svelte';
  import StateBadge from './ui/StateBadge.svelte';
  import Copy from './ui/Copy.svelte';
  import PlanView from './PlanView.svelte';
  import InstanceLog from './InstanceLog.svelte';

  let { id = $bindable('') }: { id?: string } = $props();

  let tab = $state('overview');
  let stopping = $state(false);
  const instance = $derived(id ? live.instances.get(id) : undefined);
  const alive = $derived(instanceLive(instance));
  const open = $derived(!!id);

  $effect(() => {
    if (id) tab = 'overview';
  });

  async function stop() {
    if (!instance) return;
    const yes = await confirm({ title: `Stop ${instance.name}?`, message: 'The process gets its grace period, then is killed. It will not be relaunched after a daemon restart.', action: 'Stop', tone: 'bad' });
    if (!yes) return;
    stopping = true;
    try {
      await api.instances.stopInstance({ id: instance.id });
      ok(`Stopping ${instance.name}`);
    } catch (err) {
      fail(err, 'Stop failed');
    } finally {
      stopping = false;
    }
  }

  async function again(fix: Record<string, string> = {}) {
    const r = instance?.request;
    if (!r) return;
    const tid = await launch({ ...r, params: { ...r.params, ...fix } });
    if (tid !== undefined) id = '';
  }
</script>

<Drawer
  {open}
  onOpenChange={(v) => {
    if (!v) id = '';
  }}
  title={instance?.name ?? 'Instance'}
  subtitle={instance?.id}
  width="lg"
>
  {#snippet header()}
    {#if instance}
      <div class="flex flex-wrap items-center gap-2">
        <StateBadge values={InstanceState} value={instance.state} />
        <span class="font-mono text-xs text-fg-muted">{instance.repo} · {instance.group}</span>
        <span class="text-xs text-fg-faint">on {instance.runtimeId}</span>
        {#if instance.slotId}<a href="/slots?id={instance.slotId}" class="text-xs text-accent hover:underline">slot {slotName(instance.slotId)}</a>{/if}
      </div>
      <div class="mt-3">
        <Tabs
          size="sm"
          bind:value={tab}
          tabs={[
            { id: 'overview', label: 'Overview' },
            { id: 'plan', label: 'Plan' },
            { id: 'log', label: 'Log' },
            { id: 'triage', label: 'Triage', count: instance.triage.length || undefined }
          ]}
        />
      </div>
    {/if}
  {/snippet}

  {#if instance}
    <div class="px-5 py-4">
      {#if tab === 'overview'}
        <div class="flex flex-col gap-5">
          {#if instance.error}
            <div class="rounded-lg border border-bad/30 bg-bad/10 px-3 py-2 text-sm leading-6 text-bad">{instance.error}</div>
          {/if}
          <Kv
            columns={2}
            items={[
              ['endpoint', instance.endpoint],
              ['pid', instance.pid || undefined],
              ['install', instance.installId],
              ['runtime', instance.runtimeId],
              ['created', when(instance.createdAt)],
              ['ready', instance.readyAt ? `${when(instance.readyAt)} · ${duration(instance.createdAt, instance.readyAt)} to start` : undefined],
              ['stopped', when(instance.stoppedAt)],
              ['uptime', alive ? duration(instance.readyAt ?? instance.createdAt, undefined, clock.now) : undefined],
              ['relaunch', instance.desiredRunning ? 'after restart' : 'no'],
              ['task', instance.taskId]
            ]}
          />
          {#if instance.taskId}
            <a href="/tasks?id={instance.taskId}" class="inline-flex items-center gap-1 text-xs text-accent hover:underline"><ExternalLink size={12} /> Open the launch task</a>
          {/if}

          {#if Object.keys(instance.params).length}
            <section>
              <h3 class="mb-2 text-[11px] font-semibold tracking-wider text-fg-faint uppercase">Parameters</h3>
              <div class="flex flex-wrap gap-1.5">
                {#each Object.entries(instance.params) as [k, v] (k)}
                  <span class="rounded border border-line bg-sunken px-1.5 py-0.5 font-mono text-[11px]"><span class="text-fg-faint">{k}=</span><span class="text-fg">{v}</span></span>
                {/each}
              </div>
            </section>
          {/if}

          {#if instance.measurements.length}
            <section>
              <h3 class="mb-2 text-[11px] font-semibold tracking-wider text-fg-faint uppercase">Measured allocations</h3>
              <table class="tbl">
                <thead><tr><th>key</th><th class="num">bytes</th><th>source line</th></tr></thead>
                <tbody>
                  {#each instance.measurements as m (m.key)}
                    <tr><td class="font-mono text-xs">{m.key}</td><td class="num">{bytes(m.bytes)}</td><td class="max-w-xs truncate font-mono text-xs text-fg-faint" title={m.line}>{m.line}</td></tr>
                  {/each}
                </tbody>
              </table>
            </section>
          {/if}

          {#if instance.command.length}
            <section>
              <div class="mb-2 flex items-center gap-2">
                <h3 class="text-[11px] font-semibold tracking-wider text-fg-faint uppercase">Command</h3>
                <Copy text={instance.command.join(' ')} size={12} />
              </div>
              <pre class="overflow-x-auto rounded-lg border border-line bg-sunken p-3 font-mono text-[11.5px] leading-5 whitespace-pre-wrap break-all text-fg-muted">{instance.command.join(' \\\n  ')}</pre>
            </section>
          {/if}
        </div>
      {:else if tab === 'plan'}
        {#if instance.plan}
          <PlanView plan={instance.plan} />
        {:else}
          <p class="text-sm text-fg-faint">No memory plan was recorded for this instance.</p>
        {/if}
      {:else if tab === 'log'}
        <InstanceLog id={instance.id} follow={alive} height="h-[calc(100vh-16rem)]" />
      {:else if tab === 'triage'}
        {#if instance.triage.length === 0}
          <p class="text-sm text-fg-faint">No failure patterns matched the output.</p>
        {:else}
          <div class="flex flex-col gap-3">
            {#each instance.triage as hit (hit.id)}
              <div class="rounded-lg border border-warn/30 bg-warn/8 p-3">
                <div class="text-sm font-medium text-fg">{hit.summary}</div>
                {#if hit.hint}<p class="mt-1 text-sm leading-6 text-fg-muted">{hit.hint}</p>{/if}
                {#if hit.line}<pre class="mt-2 overflow-x-auto rounded border border-line bg-sunken px-2 py-1 font-mono text-[11px] whitespace-pre-wrap text-fg-faint">{hit.line}</pre>{/if}
                {#if Object.keys(hit.fix).length}
                  <div class="mt-3 flex flex-wrap items-center gap-2">
                    <span class="text-xs text-fg-muted">suggested</span>
                    {#each Object.entries(hit.fix) as [k, v] (k)}
                      <span class="rounded border border-line bg-sunken px-1.5 py-0.5 font-mono text-[11px]"><span class="text-fg-faint">{k}=</span><span class="text-fg">{v}</span></span>
                    {/each}
                    {#if instance.request && !alive}
                      <Button size="xs" variant="primary" icon={Wrench} class="ml-auto" onclick={() => again(hit.fix)}>Run again with fix</Button>
                    {/if}
                  </div>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
      {/if}
    </div>
  {/if}

  {#snippet footer()}
    {#if instance}
      <span class="text-xs text-fg-faint">{enumLabel(InstanceState, instance.state)} · {instance.slotId ? `in slot ${slotName(instance.slotId)}` : 'standalone'}</span>
      <div class="ml-auto flex gap-2">
        {#if alive}
          <Button variant="danger" icon={Square} loading={stopping} onclick={stop}>Stop</Button>
        {:else if instance.request}
          <Button variant="primary" icon={RotateCcw} onclick={() => again()}>Run again</Button>
        {/if}
      </div>
    {/if}
  {/snippet}
</Drawer>
