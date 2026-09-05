<script lang="ts">
  import { api } from '$lib/api';
  import { live, clock, instanceLive, slotName } from '$lib/state.svelte';
  import { launch } from '$lib/launch';
  import { bytes, when, duration } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { InstanceState } from '$proto/instance_pb';
  import { Square, RotateCcw, Wrench, ExternalLink, MessageSquare } from '@lucide/svelte';
  import Drawer from './ui/Drawer.svelte';
  import Segmented from './ui/Segmented.svelte';
  import Kv from './ui/Kv.svelte';
  import Button from './ui/Button.svelte';
  import StatePill from './ui/StatePill.svelte';
  import Copy from './ui/Copy.svelte';
  import Section from './ui/Section.svelte';
  import ParamList from './ui/ParamList.svelte';
  import PlanView from './PlanView.svelte';
  import InstanceLog from './InstanceLog.svelte';

  let { id = $bindable('') }: { id?: string } = $props();

  let tab = $state('overview');
  let stopping = $state(false);
  const instance = $derived(id ? live.instances.get(id) : undefined);
  const alive = $derived(instanceLive(instance));
  const routeName = $derived(instance ? (instance.slotId ? slotName(instance.slotId) : instance.name) : '');

  $effect(() => {
    if (id) tab = instance?.state === InstanceState.FAILED && instance.triage.length ? 'triage' : 'overview';
  });

  async function stop() {
    if (!instance) return;
    const yes = await confirm({ title: `Stop ${instance.name}?`, message: 'It will not relaunch after a daemon restart.', action: 'Stop', tone: 'bad' });
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

<Drawer bind:id title={instance?.name ?? 'Instance'} subtitle={instance ? `${instance.repo} · ${instance.group}` : ''}>
  {#snippet header()}
    {#if instance}
      <div class="flex flex-wrap items-center gap-2 text-sm text-fg-muted">
        <StatePill values={InstanceState} value={instance.state} />
        <span>on {instance.runtimeId}</span>
        {#if instance.slotId}<a href="/?slot={instance.slotId}" class="link">in slot {slotName(instance.slotId)}</a>{/if}
      </div>
      <Segmented
        size="sm"
        class="mt-3"
        bind:value={tab}
        tabs={[
          { id: 'overview', label: 'Overview' },
          { id: 'plan', label: 'Plan' },
          { id: 'log', label: 'Log' },
          { id: 'triage', label: 'Triage', count: instance.triage.length || undefined }
        ]}
      />
    {/if}
  {/snippet}

  {#if instance}
    <div class="px-6 py-5">
      {#if tab === 'overview'}
        <div class="flex flex-col gap-6">
          {#if instance.error}
            <div class="note note-bad">{instance.error}</div>
          {/if}
          <Kv
            columns={2}
            items={[
              ['endpoint', instance.endpoint],
              ['pid', instance.pid || undefined],
              ['install', instance.installId],
              ['runtime', instance.runtimeId],
              ['created', when(instance.createdAt)],
              ['ready', instance.readyAt ? `${when(instance.readyAt)} · ${duration(instance.createdAt, instance.readyAt)} startup` : undefined],
              ['stopped', when(instance.stoppedAt)],
              ['uptime', alive ? duration(instance.readyAt ?? instance.createdAt, undefined, clock.now) : undefined],
              ['relaunch', instance.desiredRunning ? 'after restart' : 'no']
            ]}
          />
          {#if instance.taskId}
            <a href="/tasks?id={instance.taskId}" class="link inline-flex items-center gap-1.5 text-sm"><ExternalLink size={14} /> Launch task</a>
          {/if}

          {#if Object.keys(instance.params).length}
            <Section title="Parameters"><ParamList params={instance.params} /></Section>
          {/if}

          {#if instance.measurements.length}
            <Section title="Measured" description="What the runtime reported about itself">
              <div class="overflow-x-auto rounded-lg border border-line">
                <table class="tbl">
                  <thead><tr><th>Key</th><th class="num">Bytes</th><th>Line</th></tr></thead>
                  <tbody>
                    {#each instance.measurements as m (m.key)}
                      <tr><td class="font-mono text-xs">{m.key}</td><td class="num">{bytes(m.bytes)}</td><td class="max-w-xs truncate font-mono text-xs text-fg-faint" title={m.line}>{m.line}</td></tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </Section>
          {/if}

          {#if instance.command.length}
            <Section title="Command">
              {#snippet actions()}<Copy text={instance.command.join(' ')} size={14} />{/snippet}
              <pre class="code whitespace-pre-wrap break-all">{instance.command.join(' \\\n  ')}</pre>
            </Section>
          {/if}
        </div>
      {:else if tab === 'plan'}
        {#if instance.plan}
          <PlanView plan={instance.plan} />
        {:else}
          <p class="text-sm text-fg-faint">No plan recorded</p>
        {/if}
      {:else if tab === 'log'}
        <InstanceLog id={instance.id} follow={alive} height="h-[calc(100vh-17rem)]" />
      {:else if tab === 'triage'}
        {#if instance.triage.length === 0}
          <p class="text-sm text-fg-faint">Nothing in the log matched a known failure</p>
        {:else}
          <div class="flex flex-col gap-3">
            {#each instance.triage as hit (hit.id)}
              <div class="rounded-lg border border-warn/30 bg-warn/8 p-4">
                <div class="text-sm font-medium text-fg">{hit.summary}</div>
                {#if hit.hint}<p class="mt-1 text-sm leading-6 text-fg-muted">{hit.hint}</p>{/if}
                {#if hit.line}<pre class="code mt-3 whitespace-pre-wrap">{hit.line}</pre>{/if}
                {#if Object.keys(hit.fix).length}
                  <div class="mt-3 flex flex-wrap items-center gap-2">
                    <span class="text-sm text-fg-muted">Suggested</span>
                    <ParamList params={hit.fix} />
                    {#if instance.request && !alive}
                      <Button size="sm" variant="primary" icon={Wrench} class="ml-auto" onclick={() => again(hit.fix)}>Run with fix</Button>
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
      <span class="text-sm text-fg-faint">{instance.id.slice(0, 12)}</span>
      <div class="ml-auto flex gap-2">
        {#if alive}
          {#if instance.state === InstanceState.READY}
            <Button size="sm" icon={MessageSquare} href="/chat?model={encodeURIComponent(routeName)}">Chat</Button>
          {/if}
          <Button size="sm" variant="danger" icon={Square} loading={stopping} onclick={stop}>Stop</Button>
        {:else if instance.request}
          <Button size="sm" variant="primary" icon={RotateCcw} onclick={() => again()}>Run again</Button>
        {/if}
      </div>
    {/if}
  {/snippet}
</Drawer>
