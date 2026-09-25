<script lang="ts">
  import { page } from '$app/state';
  import { tabState } from '$lib/tabs.svelte';
  import { api } from '$lib/api';
  import { live, clock, formationByRef, formationLive, nodeName, answersOf, runtimeName, slotName } from '$lib/state.svelte';
  import { launch } from '$lib/launch';
  import { shapeLabel, seatLabel, seconds, tps, speedup, classLabel } from '$lib/mesh';
  import { bytes, when, duration, count, plural } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { FormationState } from '$proto/mesh_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { RouteState, type Trace } from '$proto/gateway_pb';
  import { Square, RotateCcw, MessageSquare, ChevronDown, ChevronRight } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Tabs from '$lib/components/ui/Tabs.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Stat from '$lib/components/ui/Stat.svelte';
  import Select from '$lib/components/ui/Select.svelte';
  import Disclosure from '$lib/components/ui/Disclosure.svelte';
  import CandidateTable from '$lib/components/CandidateTable.svelte';
  import FormationLog from '$lib/components/FormationLog.svelte';
  import TraceTable from '$lib/components/TraceTable.svelte';
  import TraceDetail from '$lib/components/TraceDetail.svelte';

  const tabs = [
    { id: 'overview', label: 'Overview' },
    { id: 'log', label: 'Log' },
    { id: 'requests', label: 'Requests' }
  ];

  const id = $derived(page.params.id ?? '');
  const formation = $derived(formationByRef(id));
  const loading = $derived(!live.ready && !live.error);
  const alive = $derived(formationLive(formation));
  const route = $derived(formation ? live.routes.get(formation.name) : undefined);
  const traces = $derived(formation ? answersOf(formation.name) : []);
  const plan = $derived(formation?.plan);
  const nodeCount = $derived(new Set(formation?.seats.map((s) => s.nodeId)).size);
  const mine = $derived(!!formation && live.nodes.get(formation.conductor)?.self === true);
  const tab = tabState(() => tabs.map((t) => t.id), () => 'overview');
  let seat = $state('');
  let expandedWorker = $state('');
  let selectedTrace = $state('');
  let stopping = $state(false);

  const workers = $derived((formation?.seats ?? []).map((s) => ({ ...s, key: `${s.nodeId}|${s.role}|${s.rank}`, label: `${seatLabel(s.role, s.rank)} · ${s.nodeName || nodeName(s.nodeId)}` })));
  const pickedSeat = $derived(workers.find((s) => s.key === seat) ?? workers.find((s) => s.role === 'head') ?? workers[0]);

  async function stop() {
    if (!formation) return;
    const yes = await confirm({ title: `Stop ${formation.name}?`, message: 'Stops all workers and disables automatic restart for this formation.', action: 'Stop', tone: 'bad' });
    if (!yes) return;
    stopping = true;
    try {
      await api.mesh.stopFormation({ id: formation.id });
      ok(`Stopping ${formation.name}`);
    } catch (err) {
      fail(err, 'Stop failed');
    } finally {
      stopping = false;
    }
  }

  async function again() {
    const r = formation?.request;
    if (!r) return;
    await launch({ ...r, params: { ...r.params } });
  }
</script>

{#if loading}
  <div class="skeleton h-40" aria-busy="true"></div>
{:else if !formation}
  <PageHeader title="Formation not found" back={{ href: '/mesh', label: 'Mesh' }} />
  <Empty title="No formation {id}">
    <Button href="/mesh">Back to Mesh</Button>
  </Empty>
{:else}
  <PageHeader title={formation.name} mono back={formation.slotId ? { href: `/slots/${formation.slotId}`, label: `Slot ${slotName(formation.slotId)}` } : { href: '/mesh', label: 'Mesh' }}>
    {#snippet meta()}
      <State values={FormationState} value={formation.state} />
      <span>{shapeLabel(formation.shape)} · {plural(nodeCount, 'node')}</span>
      <span>{formation.repo} <span class="font-mono text-xs">{formation.group}</span></span>
    {/snippet}
    {#if alive}
      {#if route?.state === RouteState.READY}<Button icon={MessageSquare} href="/chat?model={encodeURIComponent(formation.name)}">Chat</Button>{/if}
      {#if mine}<Button variant="danger" icon={Square} loading={stopping} onclick={stop}>Stop</Button>{/if}
    {:else if formation.request && mine}
      <Button variant="primary" icon={RotateCcw} onclick={() => again()}>Run again</Button>
    {/if}
    {#snippet below()}
      <Tabs tabs={tabs.map((t) => (t.id === 'requests' ? { ...t, count: traces.length || undefined } : t))} bind:value={tab.value} />
    {/snippet}
  </PageHeader>

  {#if tab.value === 'overview'}
    <div class="flex flex-col gap-5">
      {#if formation.error}<div class="note note-bad">{formation.error}</div>{/if}
      {#if !mine}<div class="note">To stop or restart this formation, use {formation.conductorName || nodeName(formation.conductor)}.</div>{/if}
      <div class="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <Stat label="Coordinator" value={formation.conductorName || nodeName(formation.conductor)} />
        <Stat label="Runtime" value={runtimeName(formation.runtimeId)} />
        <Stat label="Uptime" value={alive && formation.readyAt ? duration(formation.readyAt, undefined, clock.now) : '–'} />
        <Stat label="Requests" value={count(route?.requests ?? 0n)} sub={route?.inFlight ? `${route.inFlight} in flight` : ''} />
      </div>
      <Card title="Workers">
        <div class="tbl-wrap contain-inline-size">
          <table class="tbl">
            <thead><tr><th>Worker</th><th>Node</th><th>State</th><th>Layers</th><th class="num">Weights</th><th class="num">Cache</th><th><span class="sr-only">Connection details</span></th></tr></thead>
            <tbody>
              {#each workers as s (s.key)}
                <tr>
                  <td class="whitespace-nowrap text-fg">{#if s.instanceId}<a class="link" href="/instances/{s.instanceId}">{seatLabel(s.role, s.rank)}</a>{:else}{seatLabel(s.role, s.rank)}{/if}</td>
                  <td class="text-fg">{s.nodeName || nodeName(s.nodeId)}{#if s.exposed}<span class="ml-2 text-xs text-warn" title="Reachable directly on the mesh address">Direct</span>{/if}</td>
                  <td><State values={InstanceState} value={s.state} /></td>
                  <td class="tabular-nums text-fg-muted">{s.layerTo > s.layerFrom ? `${s.layerFrom}–${s.layerTo - 1}` : 'All'}</td>
                  <td class="num">{bytes(s.weightBytes)}</td>
                  <td class="num">{bytes(s.cacheBytes)}</td>
                  <td class="actions"><IconButton size="xs" icon={expandedWorker === s.key ? ChevronDown : ChevronRight} label="{expandedWorker === s.key ? 'Hide' : 'Show'} {seatLabel(s.role, s.rank)} connection details" aria-expanded={expandedWorker === s.key} onclick={() => (expandedWorker = expandedWorker === s.key ? '' : s.key)} /></td>
                </tr>
                {#if s.error || expandedWorker === s.key}
                  <tr>
                    <td colspan="7">
                      {#if s.error}<p class="text-sm leading-6 text-bad wrap-anywhere">{s.error}</p>{/if}
                      {#if expandedWorker === s.key}<Kv class={s.error ? 'mt-3' : ''} items={[["Endpoint", s.endpoint], ["Transport", s.transport]]} />{/if}
                    </td>
                  </tr>
                {/if}
              {:else}
                <tr><td colspan="7" class="text-fg-faint">No workers have started</td></tr>
              {/each}
            </tbody>
          </table>
        </div>
      </Card>
      <div class="grid grid-cols-1 gap-5 xl:grid-cols-2">
        <Card title="Details">
          <Kv
            items={[
              ['Model', `${formation.repo} · ${formation.group}`],
              ['Source', formation.sourceId],
              ['Endpoint', formation.endpoint],
              ['Data transferred', formation.bytesMoved ? bytes(formation.bytesMoved) : ''],
              ['Slot', formation.slotId ? slotName(formation.slotId) : ''],
              ['Created', when(formation.createdAt)],
              ['Ready', when(formation.readyAt)],
              ['Stopped', when(formation.stoppedAt)],
              ['Auto-restart', formation.desiredRunning ? 'Enabled' : 'Disabled']
            ]}
            omitEmpty
          />
          {#if formation.taskId}<a href="/tasks/{formation.taskId}" class="link mt-3 inline-block text-sm">Open launch task</a>{/if}
        </Card>
        {#if plan}
          <Card title="Performance estimates">
            <div class="flex flex-col gap-4">
              <div class="grid grid-cols-3 gap-4">
                <Stat label="First token" value={seconds(plan.prefillSeconds)} />
                <Stat label="Tokens/s" value={tps(plan.tokensPerSecond)} />
                <Stat label="Speedup" value={speedup(plan.speedup)} sub="vs. one node" />
              </div>
              {#if plan.referencePrompt}<p class="text-xs leading-5 text-fg-muted">Based on {plan.referencePrompt.toLocaleString()} prompt and {plan.referenceCompletion.toLocaleString()} output tokens.</p>{/if}
              <Kv items={[
                ['Context', plan.context ? `${plan.context.toLocaleString()} tokens` : ''],
                ['Network', classLabel(plan.linkClass)],
                ['Requests/s', plan.requestsPerSecond > 0 ? plan.requestsPerSecond.toFixed(2) : '']
              ]} omitEmpty />
              <Disclosure label="Estimate details">
                <div class="flex flex-col gap-3">
                  <Kv items={[
                    ['Draft tokens per round', plan.draftTokens || ''],
                    ['Draft acceptance', plan.draftTokens ? `${Math.round(plan.acceptance * 100)}%` : ''],
                    ['Relay threshold', plan.relayBreakEvenPrompt ? `${plan.relayBreakEvenPrompt.toLocaleString()} prompt tokens` : ''],
                    ['First-token adjustment', plan.ttftRatio > 0 && plan.ttftRatio !== 1 ? speedup(plan.ttftRatio) : ''],
                    ['Per-token adjustment', plan.tptRatio > 0 && plan.tptRatio !== 1 ? speedup(plan.tptRatio) : '']
                  ]} omitEmpty />
                  {#if plan.detail}<p class="text-sm leading-6 text-fg-muted wrap-anywhere">{plan.detail}</p>{/if}
                  {#if plan.sources.length}
                    <div>
                      <div class="caps mb-2 text-fg-faint">Sources</div>
                      <ul class="flex flex-col gap-2 text-sm leading-6 text-fg-muted">
                        {#each plan.sources as source (source)}<li class="wrap-anywhere">{source}</li>{/each}
                      </ul>
                    </div>
                  {/if}
                </div>
              </Disclosure>
            </div>
          </Card>
        {/if}
      </div>
      {#if plan}
        <Card title="Distribution comparison">
          <CandidateTable {plan} />
        </Card>
      {/if}
    </div>
  {:else if tab.value === 'log'}
    <div class="flex flex-col gap-3">
      {#if workers.length > 1}<Select label="Worker" size="sm" class="w-full sm:w-80" bind:value={() => pickedSeat?.key ?? '', (value) => (seat = value)} items={workers.map((s) => ({ value: s.key, label: s.label }))} />{/if}
      {#if pickedSeat}
        <FormationLog id={formation.id} nodeId={pickedSeat.nodeId} role={pickedSeat.role} follow={alive} height="h-[calc(100vh-20rem)]" />
      {:else}
        <Empty compact title="No workers have started" />
      {/if}
    </div>
  {:else if tab.value === 'requests'}
    <div class="grid grid-cols-1 gap-6 {selectedTrace ? 'xl:grid-cols-[minmax(0,1fr)_28rem]' : ''}">
      <TraceTable {traces} selected={selectedTrace} onSelect={(t: Trace) => (selectedTrace = selectedTrace === t.id ? '' : t.id)} compact={!!selectedTrace} />
      {#if selectedTrace}
        <Card title="Request" class="xl:sticky xl:top-8 xl:self-start"><TraceDetail id={selectedTrace} /></Card>
      {/if}
    </div>
  {/if}
{/if}
