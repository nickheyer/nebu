<script lang="ts">
  import { page } from '$app/state';
  import { tabState } from '$lib/tabs.svelte';
  import { live, clock, instanceLive, slotName, groupLabel, runtimeName, answersOf } from '$lib/state.svelte';
  import { launch } from '$lib/launch';
  import { stopInstance } from '$lib/actions.svelte';
  import { bytes, when, duration, count, commandLines, tail } from '$lib/format';
  import { InstanceState } from '$proto/instance_pb';
  import { RouteState, type Trace } from '$proto/gateway_pb';
  import { Square, RotateCcw, Wrench, MessageSquare } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Tabs from '$lib/components/ui/Tabs.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Copy from '$lib/components/ui/Copy.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Stat from '$lib/components/ui/Stat.svelte';
  import ParamList from '$lib/components/ui/ParamList.svelte';
  import PlanView from '$lib/components/PlanView.svelte';
  import InstanceLog from '$lib/components/InstanceLog.svelte';
  import TraceTable from '$lib/components/TraceTable.svelte';
  import TraceDetail from '$lib/components/TraceDetail.svelte';

  const tabs = [
    { id: 'overview', label: 'Overview' },
    { id: 'log', label: 'Log' },
    { id: 'requests', label: 'Requests' },
    { id: 'triage', label: 'Triage' }
  ];

  const id = $derived(page.params.id ?? '');
  const instance = $derived(live.instances.get(id) ?? [...live.instances.values()].find((i) => i.name === id));
  const loading = $derived(!live.ready && !live.error);
  const alive = $derived(instanceLive(instance));
  const routeName = $derived(instance ? (instance.slotId ? slotName(instance.slotId) : instance.name) : '');
  const route = $derived(routeName ? live.routes.get(routeName) : undefined);
  const install = $derived(instance?.installId ? live.installs.get(instance.installId) : undefined);
  const traces = $derived(instance ? answersOf().filter((t) => t.instanceId === instance.id) : []);
  // The model behind the name, said once, when the name does not already say it
  const reference = $derived(instance ? `${instance.repo} ${groupLabel(instance)}` : '');
  const named = $derived(!!instance && [instance.repo + ':' + instance.group, tail(instance.repo) + ':' + instance.group, reference].includes(instance.name));
  const tab = tabState(() => tabs.map((t) => t.id), () => 'overview');
  let selectedTrace = $state('');
  let stopping = $state(false);
  // A failed instance opens on its triage once, when no tab was asked for
  let shownTriage = false;
  $effect(() => {
    if (shownTriage || !instance || page.url.searchParams.get('tab')) return;
    if (instance.state === InstanceState.FAILED && instance.triage.length) {
      shownTriage = true;
      tab.value = 'triage';
    }
  });

  async function stop() {
    if (!instance) return;
    stopping = true;
    await stopInstance(instance.id, instance.name);
    stopping = false;
  }

  async function again(fix: Record<string, string> = {}) {
    const r = instance?.request;
    if (!r) return;
    await launch({ ...r, params: { ...r.params, ...fix } });
  }
</script>

{#if loading}
  <div class="skeleton h-40" aria-busy="true"></div>
{:else if !instance}
  <PageHeader title="Instance not found" back={{ href: '/', label: 'Serve' }} />
  <Empty title="No instance {id}">
    <Button href="/">Back to Serve</Button>
  </Empty>
{:else}
  <PageHeader title={instance.name} mono back={instance.slotId ? { href: `/slots/${instance.slotId}`, label: `Slot ${slotName(instance.slotId)}` } : { href: '/', label: 'Serve' }}>
    {#snippet meta()}
      <State values={InstanceState} value={instance.state} />
      {#if !named}<span>{instance.repo} <span class="font-mono text-xs">{groupLabel(instance)}</span></span>{/if}
    {/snippet}
    {#if alive}
      {#if route?.state === RouteState.READY}<Button icon={MessageSquare} href="/chat?model={encodeURIComponent(routeName)}">Chat</Button>{/if}
      <Button variant="danger" icon={Square} loading={stopping} onclick={stop}>Stop</Button>
    {:else if instance.request}
      <Button variant="primary" icon={RotateCcw} onclick={() => again()}>Run again</Button>
    {/if}
    {#snippet below()}
      <Tabs tabs={tabs.map((t) => (t.id === 'triage' ? { ...t, count: instance.triage.length || undefined } : t.id === 'requests' ? { ...t, count: traces.length || undefined } : t))} bind:value={tab.value} />
    {/snippet}
  </PageHeader>

  {#if tab.value === 'overview'}
    <div class="flex flex-col gap-5">
      {#if instance.error}<div class="note note-bad">{instance.error}</div>{/if}
      <div class="grid grid-cols-2 gap-4 sm:grid-cols-4 xl:grid-cols-6">
        <Stat label="Runtime" value={runtimeName(instance.runtimeId)} sub={install?.version} />
        <Stat label="Endpoint" value={instance.endpoint || '–'} mono />
        <Stat label="PID" value={instance.pid || '–'} mono />
        <Stat label="Uptime" value={alive ? duration(instance.readyAt ?? instance.createdAt, undefined, clock.now) : '–'} />
        <Stat label="Startup" value={instance.readyAt ? duration(instance.createdAt, instance.readyAt) : '–'} />
        <Stat label="Requests" value={count(route?.requests ?? 0n)} sub={route?.inFlight ? `${route.inFlight} in flight` : ''} />
      </div>
      {#if instance.plan}
        <Card title="Memory">
          <PlanView plan={instance.plan} params={false} except={instance.id} />
        </Card>
      {/if}
      <div class="grid grid-cols-1 gap-5 xl:grid-cols-2">
        <Card title="Details">
          <Kv
            items={[
              ['Model', `${instance.repo} · ${groupLabel(instance)}`],
              ['Source', instance.sourceId],
              ['Install', install ? `${install.version || install.id} · ${install.path}` : instance.installId],
              ['Slot', instance.slotId ? slotName(instance.slotId) : 'None'],
              ['Created', when(instance.createdAt)],
              ['Ready', when(instance.readyAt)],
              ['Stopped', when(instance.stoppedAt)],
              ['After a restart', instance.desiredRunning ? 'Comes back' : 'Stays stopped'],
              ['Task', instance.taskId]
            ]}
          />
          {#if instance.taskId}<a href="/tasks/{instance.taskId}" class="link mt-3 inline-block text-sm">Open launch task</a>{/if}
        </Card>
        <div class="flex flex-col gap-5">
          {#if Object.keys(instance.params).length}
            <Card title="Parameters"><ParamList params={instance.params} /></Card>
          {/if}
          {#if instance.measurements.length}
            <Card title="Measured" meta="what the runtime reported" padded={false}>
              <div class="px-3">
                <table class="tbl">
                  <thead><tr><th>Key</th><th class="num">Bytes</th><th>Line</th></tr></thead>
                  <tbody>
                    {#each instance.measurements as m (m.key)}
                      <tr><td class="font-mono text-xs">{m.key}</td><td class="num">{bytes(m.bytes)}</td><td class="max-w-xs truncate font-mono text-xs text-fg-faint" title={m.line}>{m.line}</td></tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </Card>
          {/if}
        </div>
      </div>
      {#if instance.command.length}
        <Card title="Command">
          {#snippet actions()}<Copy text={instance.command.join(' ')} size={13} /> {/snippet}
          <pre class="code whitespace-pre-wrap wrap-anywhere">{commandLines(instance.command)}</pre>
        </Card>
      {/if}
    </div>
  {:else if tab.value === 'log'}
    <InstanceLog id={instance.id} follow={alive} height="h-[calc(100vh-18rem)]" />
  {:else if tab.value === 'requests'}
    <div class="grid grid-cols-1 gap-6 {selectedTrace ? 'xl:grid-cols-[minmax(0,1fr)_28rem]' : ''}">
      <TraceTable {traces} selected={selectedTrace} onSelect={(t: Trace) => (selectedTrace = selectedTrace === t.id ? '' : t.id)} compact={!!selectedTrace} />
      {#if selectedTrace}
        <Card title="Request" class="xl:sticky xl:top-8 xl:self-start"><TraceDetail id={selectedTrace} /></Card>
      {/if}
    </div>
  {:else if tab.value === 'triage'}
    {#if instance.triage.length === 0}
      <Empty compact title="No known failure pattern matched the log" />
    {:else}
      <div class="flex flex-col gap-3">
        {#each instance.triage as hit (hit.id)}
          <div class="rounded-md border border-warn/25 bg-warn/8 p-4">
            <div class="text-sm font-medium text-fg">{hit.summary}</div>
            {#if hit.hint}<p class="mt-1 text-sm leading-6 text-fg-muted">{hit.hint}</p>{/if}
            {#if hit.line}<pre class="code mt-3 whitespace-pre-wrap">{hit.line}</pre>{/if}
            {#if Object.keys(hit.fix).length}
              <div class="mt-3 flex flex-wrap items-center gap-3">
                <ParamList params={hit.fix} />
                {#if instance.request && !alive}
                  <Button size="sm" variant="primary" icon={Wrench} class="ml-auto" onclick={() => again(hit.fix)}>Run again with this fix</Button>
                {/if}
              </div>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  {/if}
{/if}
