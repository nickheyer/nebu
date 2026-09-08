<script lang="ts">
  import { untrack } from 'svelte';
  import { Code } from '@connectrpc/connect';
  import { api, code, message } from '$lib/api';
  import { live, clock, taskFor, modelKey, hostName, runtimeName, instanceLive, installsOf, storeMount, poolName, orderedSlots, startedTask } from '$lib/state.svelte';
  import { runModel } from '$lib/actions.svelte';
  import { ago, ratioBytes, ratioStorage, storage, count, params as fmtParams, enumLabel, byName, ctx as fmtCtx } from '$lib/format';
  import { readLocal, writeLocal } from '$lib/persist';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { descriptorKey, facetValueLabel, hitChips, locked, orderDescriptors, precisionShort, rowAt, weightsName } from '$lib/catalog';
  import { FitVerdict, type InspectResponse, type MemoryPlan } from '$proto/estimate_pb';
  import { PoolKind } from '$proto/host_pb';
  import type { SearchHit, SourceCapabilities, Revision, ModelCard } from '$proto/source_pb';
  import type { StoredModel } from '$proto/store_pb';
  import { ArtifactRole, type Descriptor } from '$proto/model_pb';
  import { Download, Play, Check, ChevronRight, ExternalLink, ArrowDownToLine, Heart, RefreshCw, Lock, ShieldCheck, FolderOutput, Trash2, Files } from '@lucide/svelte';
  import Drawer from './ui/Drawer.svelte';
  import Tabs from './ui/Tabs.svelte';
  import Segmented from './ui/Segmented.svelte';
  import Select from './ui/Select.svelte';
  import Button from './ui/Button.svelte';
  import Skeleton from './ui/Skeleton.svelte';
  import SkeletonRows from './ui/SkeletonRows.svelte';
  import Empty from './ui/Empty.svelte';
  import Kv from './ui/Kv.svelte';
  import Markdown from './ui/Markdown.svelte';
  import Tip from './ui/Tip.svelte';
  import State from './ui/State.svelte';
  import Disclosure from './ui/Disclosure.svelte';
  import StackBar from './ui/StackBar.svelte';
  import PlanTable from './PlanTable.svelte';
  import ModelFiles from './ModelFiles.svelte';
  import TaskChip from './TaskChip.svelte';
  import TextInput from './ui/TextInput.svelte';

  // One repository: whether its weights run on this host and fit on its disk, what is stored of it, its card, and its files
  let {
    open = $bindable(false),
    sourceId,
    sourceLabel = '',
    repo,
    revision = '',
    caps,
    hit = null,
    slotId = $bindable(''),
    onNavigate
  }: {
    open?: boolean;
    sourceId: string;
    sourceLabel?: string;
    repo: string;
    revision?: string;
    caps?: SourceCapabilities;
    hit?: SearchHit | null;
    slotId?: string;
    onNavigate?: (repo: string, revision: string) => void;
  } = $props();

  let curRepo = $state('');
  let curRev = $state('');
  let tab = $state('weights');
  let inspect = $state<InspectResponse | null>(null);
  let inspectError = $state('');
  let denied = $state(false);
  let inspecting = $state(false);
  let revisions = $state<Revision[]>([]);
  let card = $state<ModelCard | null>(null);
  let cardError = $state('');
  let showWarnings = $state(false);
  let exportDir = $state('');
  let exporting = $state('');
  let generation = 0;

  const model = $derived(inspect?.model);
  const revLabel = $derived(caps?.revisionLabel || 'revision');
  const currentRevision = $derived(revisions.find((r) => (r.repo ? r.repo === (model?.repo ?? curRepo) : r.name === (model?.revision ?? curRev))) ?? revisions.find((r) => r.default));
  const revisionText = $derived(currentRevision?.name || model?.revision || curRev || 'default');
  const pageUrl = $derived(hit?.url || card?.url || '');
  const siteName = $derived(sourceLabel || caps?.name || 'the source');
  // The namespace ahead of the slash, a link back into the catalog where the source can filter by it
  const author = $derived(hit?.author || (curRepo.includes('/') ? curRepo.slice(0, curRepo.indexOf('/')) : ''));
  const rest = $derived(author && curRepo.startsWith(author + '/') ? curRepo.slice(author.length + 1) : curRepo);
  const authorHref = $derived(author && caps?.facets.some((f) => f.id === 'author') ? `/catalog?source=${encodeURIComponent(sourceId)}&f.author=${encodeURIComponent(author)}` : '');
  const slots = $derived(orderedSlots());
  const installed = (id: string) => installsOf(id).length > 0;
  // The runtime the table is read on: the pick, else the first with an install, else the first planned
  let pickedRuntime = $state('');
  const planRuntimes = $derived([...new Set((inspect?.rows ?? []).map((r) => r.runtimeId))]);
  const runtime = $derived(planRuntimes.includes(pickedRuntime) ? pickedRuntime : (planRuntimes.find(installed) ?? planRuntimes[0] ?? ''));
  const rows = $derived((inspect?.rows ?? []).filter((r) => r.runtimeId === runtime));
  // The context length the table is read at: the last pick, kept per browser, else the shortest planned, which is the runtime default
  const ctxKey = 'nebu.drawer.context';
  let pickedCtx = $state(Number(readLocal(ctxKey)) || 0);
  const contexts = $derived([...new Set(rows.map((r) => r.context))].sort((a, b) => a - b));
  const context = $derived(contexts.includes(pickedCtx) ? pickedCtx : (contexts[0] ?? 0));
  function pickContext(c: number) {
    pickedCtx = c;
    writeLocal(ctxKey, String(c));
  }
  const ordered = $derived(inspect ? orderDescriptors(inspect.descriptors, rows, context) : []);
  const names = $derived(Object.fromEntries((inspect?.descriptors ?? []).map((d) => [d.group, weightsName(d.group, d.formatId)])));
  const cells = $derived(new Map(ordered.map((d) => [d.group, rowAt(rows, d.group, context)])));
  // The first row that fits is the largest that does, the usual thing to pull
  const bestGroup = $derived(ordered.find((d) => cells.get(d.group)?.plan?.verdict === FitVerdict.FITS)?.group ?? '');
  // The source's count when it has one, else what the headers add up to
  const paramCount = $derived(hit && hit.parameters > 0n ? hit.parameters : ordered.reduce((a, d) => (d.parameterCount > a ? d.parameterCount : a), 0n));
  const limit = $derived(Math.max(0, ...ordered.map((d) => d.params['n_ctx_train'] ?? 0)));
  const atLimit = $derived(limit > 0 && contexts.at(-1) === limit);
  const mount = $derived(storeMount());
  // A gated repository on a source without a token cannot be read, and a refusal says the same
  const gated = $derived(locked(hit, caps) || denied);
  const chips = $derived(hit ? hitChips(hit, caps) : []);
  const storedGroups = $derived([...live.models.values()].filter((m) => m.sourceId === sourceId && m.repo === curRepo).sort(byName((m) => m.group)));
  // The paths of this repository already in the store, across every variant pulled
  const storedPaths = $derived(new Set(storedGroups.flatMap((m) => m.artifacts.map((a) => a.artifact?.path ?? ''))));
  const tabs = $derived([
    { id: 'weights', label: 'Weights' },
    ...(storedGroups.length ? [{ id: 'stored', label: 'Downloaded', count: storedGroups.length }] : []),
    ...(caps?.card ? [{ id: 'card', label: 'Card' }] : []),
    { id: 'files', label: 'Files', count: model ? model.artifacts.length : undefined }
  ]);
  const shownTab = $derived(tabs.some((t) => t.id === tab) ? tab : 'weights');
  const formatIds = $derived([...new Set((inspect?.descriptors ?? []).map((d) => d.formatId))]);
  // Nothing installed serves these weights, so the table plans on what could be installed and says so once
  const noRuntime = $derived(planRuntimes.length > 0 && !planRuntimes.some(installed));
  const poolWord: Record<number, string> = { [PoolKind.DEVICE]: 'GPU', [PoolKind.HOST]: 'RAM', [PoolKind.UNIFIED]: 'MEM' };
  // One bar per pool with what the plan put there, a unified pool listed twice carrying both of its shares; a
  // plan nothing holds fills each pool in turn and runs the last one past its capacity, which the bar shows
  function poolRows(plan: MemoryPlan): { id: string; kind: PoolKind; used: bigint; cap: bigint }[] {
    const out: { id: string; kind: PoolKind; used: bigint; cap: bigint }[] = [];
    for (const p of plan.pools) {
      const row = out.find((r) => r.id === p.poolId);
      if (row) row.used += p.usedBytes;
      else out.push({ id: p.poolId, kind: p.kind, used: p.usedBytes, cap: p.capacityBytes });
    }
    return out;
  }
  // The row opened to its plan
  let expanded = $state('');

  // A new target resets everything, the same target keeps what is loaded
  $effect(() => {
    const o = open;
    const r = repo;
    const v = revision;
    untrack(() => {
      if (!o) return;
      if (r === curRepo && v === curRev && (inspect || inspecting || gated)) return;
      curRepo = r;
      curRev = v;
      load(false);
    });
  });

  // Another revision keeps the tab, another repository starts on the weights
  async function load(keepTab: boolean) {
    const gen = ++generation;
    if (!keepTab) tab = 'weights';
    if (gated && caps?.card) tab = 'card';
    inspect = null;
    inspectError = '';
    denied = false;
    revisions = [];
    card = null;
    cardError = '';
    showWarnings = false;
    planned = slotId;
    const target = { sourceId, repo: curRepo, revision: curRev };
    const jobs: Promise<void>[] = [];
    if (!gated) {
      inspecting = true;
      jobs.push(
        api.estimate
          .inspect({ ...target, slotId })
          .then((r) => {
            if (gen !== generation) return;
            inspect = r;
          })
          .catch((err) => {
            if (gen !== generation) return;
            if (code(err) === Code.PermissionDenied) denied = true;
            else inspectError = message(err);
          })
          .finally(() => {
            if (gen === generation) inspecting = false;
          })
      );
      if (caps?.revisions) {
        jobs.push(
          api.sources
            .listRevisions({ sourceId, repo: curRepo })
            .then((r) => {
              if (gen === generation) revisions = r.revisions;
            })
            .catch(() => {})
        );
      }
    }
    if (caps?.card) {
      jobs.push(
        api.sources
          .getModelCard(target)
          .then((r) => {
            if (gen === generation) card = r.card ?? null;
          })
          .catch((err) => {
            if (gen === generation) cardError = message(err);
          })
      );
    }
    await Promise.all(jobs);
  }

  // Plans again when the slot changes, the listing and headers being cached daemon side
  let planned = '';
  $effect(() => {
    const key = slotId;
    const due = untrack(() => {
      if (key === planned) return false;
      planned = key;
      return !!inspect;
    });
    if (!due) return;
    const timer = setTimeout(refit, 350);
    return () => clearTimeout(timer);
  });
  async function refit() {
    const gen = ++generation;
    inspecting = true;
    try {
      const r = await api.estimate.inspect({ sourceId, repo: curRepo, revision: curRev, slotId });
      if (gen !== generation) return;
      inspect = r;
    } catch (err) {
      if (gen === generation) inspectError = message(err);
    } finally {
      if (gen === generation) inspecting = false;
    }
  }

  function pick(r: Revision) {
    if (r.repo && r.repo !== curRepo) {
      curRepo = r.repo;
      curRev = '';
    } else if (!r.repo) {
      curRev = r.name;
    } else {
      return;
    }
    onNavigate?.(curRepo, curRev);
    load(true);
  }

  async function pull(d: Descriptor) {
    if (!model) return;
    try {
      const r = await api.store.pull({ sourceId, repo: model.repo, revision: model.revision, group: d.group });
      startedTask(`Downloading ${model.repo}`, `Downloaded ${model.repo}`, names[d.group], r.task);
    } catch (err) {
      fail(err, 'Download refused');
    }
  }
  function storedModel(group: string) {
    return live.models.get(modelKey({ sourceId, repo: curRepo, group }));
  }
  function pulling(group: string) {
    return model ? taskFor('pull', { source: sourceId, repo: model.repo, group }) : undefined;
  }
  function servingAs(m: StoredModel): string[] {
    return [...live.instances.values()].filter((i) => i.sourceId === m.sourceId && i.repo === m.repo && i.group === m.group && instanceLive(i)).map((i) => (i.slotId ? (live.slots.get(i.slotId)?.name ?? i.name) : i.name));
  }

  async function verify(m: StoredModel) {
    try {
      const r = await api.store.verify({ sourceId: m.sourceId, repo: m.repo, group: m.group });
      startedTask(`Verifying ${weightsName(m.group, m.formatId)}`, `Verified ${weightsName(m.group, m.formatId)}`, undefined, r.task);
    } catch (err) {
      fail(err, 'Verify refused');
    }
  }
  async function exportModel(m: StoredModel) {
    exporting = m.group;
    try {
      const r = await api.store.export({ sourceId: m.sourceId, repo: m.repo, group: m.group, dir: exportDir.trim() });
      startedTask(`Exporting ${weightsName(m.group, m.formatId)}`, `Exported ${weightsName(m.group, m.formatId)}`, exportDir.trim(), r.task);
    } catch (err) {
      fail(err, 'Export refused');
    } finally {
      exporting = '';
    }
  }
  async function remove(m: StoredModel) {
    const yes = await confirm({ title: `Remove ${weightsName(m.group, m.formatId)}?`, message: 'Files not shared with another variant are deleted from disk.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      const r = await api.store.removeModel({ sourceId: m.sourceId, repo: m.repo, group: m.group, gc: true });
      live.models.delete(modelKey(m));
      ok(`Removed ${weightsName(m.group, m.formatId)}`, r.gc ? `${storage(r.gc.freedBytes)} freed` : undefined);
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }
</script>

<Drawer bind:open mono title={curRepo || 'Model'}>
  {#snippet heading()}
    {#if author}{#if authorHref}<a href={authorHref} class="text-fg-muted transition-colors hover:text-fg" title="All models by {author} on {siteName}">{author}</a>{:else}<span class="text-fg-muted">{author}</span>{/if}<span class="text-fg-muted">/</span>{/if}{#if pageUrl}<a href={pageUrl} target="_blank" rel="noopener noreferrer" class="group transition-colors hover:text-accent">{rest}<span class="ml-2 font-sans text-xs font-normal text-fg-faint opacity-0 transition-opacity group-hover:opacity-100">open on {siteName}</span></a>{:else}{rest}{/if}
  {/snippet}
  {#snippet header()}
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-fg-muted">
      {#if hit?.task}<span class="text-fg">{facetValueLabel(caps, 'task', hit.task)}</span>{/if}
      {#if hit && hit.downloads > 0n}<span class="inline-flex items-center gap-1 tabular-nums"><ArrowDownToLine size={12} />{count(hit.downloads)}</span>{/if}
      {#if hit && hit.likes > 0n}<span class="inline-flex items-center gap-1 tabular-nums"><Heart size={12} />{count(hit.likes)}</span>{/if}
      {#if paramCount > 0n}<span>{fmtParams(paramCount)} params</span>{:else if hit && hit.sizeBytes > 0n}<span>{storage(hit.sizeBytes, 1)}</span>{/if}
      {#if hit?.license}<span><span class="text-fg-faint">license</span> {hit.license}</span>{/if}
      {#if hit?.updatedAt}<span><span class="text-fg-faint">updated</span> {ago(hit.updatedAt, clock.now)}</span>{/if}
      {#if revisions.length > 1}
        <span class="ml-auto inline-flex items-center gap-2 text-xs text-fg-faint">
          {revLabel}
          <Select
            size="sm"
            mono
            class="w-auto max-w-[14rem]"
            label={revLabel}
            value={currentRevision?.name ?? ''}
            onchange={(name) => {
              const r = revisions.find((x) => x.name === name);
              if (r) pick(r);
            }}
            items={revisions.map((r) => ({ value: r.name, label: r.name, detail: [r.sizeBytes ? storage(r.sizeBytes, 1) : '', r.updatedAt ? ago(r.updatedAt, clock.now) : ''].filter(Boolean).join(' · ') || undefined }))}
          />
        </span>
      {:else if !gated}
        <span class="ml-auto text-xs text-fg-faint">{revLabel} <span class="font-mono text-fg-muted">{revisionText}</span>{#if model?.commit}<span class="ml-2 font-mono" title={model.commit}>{model.commit.slice(0, 12)}</span>{/if}</span>
      {/if}
    </div>
    {#if chips.length}
      <div class="mt-2 flex flex-wrap gap-1.5">
        {#each chips as c (c)}<span class="rounded-sm bg-raised px-1.5 py-0.5 text-[11px] text-fg-muted">{c}</span>{/each}
      </div>
    {/if}
    <Tabs size="sm" bind:value={() => shownTab, (v) => (tab = v)} {tabs} class="mt-3" />
  {/snippet}

  <div class="px-6 py-5">
    {#if shownTab === 'weights'}
      {#if gated}
        <Empty icon={Lock} title="Gated on {siteName}">
          {#if pageUrl}<Button size="sm" href={pageUrl} icon={ExternalLink}>Accept the license on {siteName}</Button>{/if}
          <span class="text-xs text-fg-faint">Then set {caps?.tokenEnv || 'a token'} for the daemon.</span>
        </Empty>
      {:else if inspecting && !inspect}
        <table class="tbl" aria-busy="true">
          <thead><tr><th>Variant</th><th></th><th></th></tr></thead>
          <tbody><SkeletonRows rows={3} cols={[{ w: 'w-32' }, 'w-56', { w: 'w-12', num: true }]} /></tbody>
        </table>
      {:else if inspectError}
        <Empty compact title={inspectError}>
          <Button size="sm" icon={RefreshCw} onclick={() => load(true)}>Retry</Button>
        </Empty>
      {:else if inspect && model}
        <div class="flex flex-col gap-6">
          {#if noRuntime}
            <div class="note note-warn flex flex-wrap items-center gap-3">
              <span class="flex-1">No installed runtime reads {formatIds.join(', ')}. You can download the weights now and run them once one is installed.</span>
              <Button size="sm" href="/runtimes/{runtime}?tab=install" icon={Download}>Install {runtimeName(runtime)}</Button>
            </div>
          {:else if ordered.length && !planRuntimes.length}
            <div class="note note-warn">No runtime reads {formatIds.join(', ')}.</div>
          {/if}
          {#if ordered.length}
            {#if contexts.length > 1 || planRuntimes.length > 1 || slots.length}
              <div class="flex flex-wrap items-center gap-x-5 gap-y-2 text-xs text-fg-faint">
                {#if contexts.length > 1}
                  <span class="inline-flex items-center gap-2">
                    <span>Context</span>
                    <Segmented size="sm" tabs={contexts.map((c) => ({ id: String(c), label: fmtCtx(c) }))} bind:value={() => String(context), (v) => pickContext(Number(v))} />
                    {#if atLimit}<span>{fmtCtx(limit)} is the model's maximum</span>{/if}
                  </span>
                {/if}
                {#if planRuntimes.length > 1}
                  <span class="inline-flex items-center gap-2">
                    <span>Runtime</span>
                    <Segmented size="sm" tabs={planRuntimes.map((id) => ({ id, label: runtimeName(id) }))} bind:value={() => runtime, (v) => (pickedRuntime = v)} />
                  </span>
                {/if}
                {#if slots.length}
                  <span class="inline-flex items-center gap-2">
                    <span>Fit in</span>
                    <Segmented size="sm" bind:value={slotId} tabs={[{ id: '', label: hostName() || 'Whole host' }, ...slots.map((s) => ({ id: s.id, label: s.name }))]} />
                    {#if inspecting}<span>planning…</span>{/if}
                  </span>
                {/if}
              </div>
            {/if}
            <table class="tbl">
              <thead><tr><th>Variant</th><th></th><th></th></tr></thead>
              <tbody>
                {#each ordered as d (descriptorKey(d))}
                  {@const stored = storedModel(d.group)}
                  {@const task = pulling(d.group)}
                  {@const p = d.precision}
                  {@const plan = cells.get(d.group)?.plan}
                  {@const best = d.group === bestGroup && ordered.length > 1}
                  {@const room = !mount || d.totalBytes <= mount.freeBytes}
                  {@const open = expanded === d.group}
                  <tr class="row-link {open ? 'row-active' : ''}" onclick={() => (expanded = open ? '' : d.group)}>
                    <td class="whitespace-nowrap">
                      <div class="flex items-center gap-2">
                        <ChevronRight size={12} class="shrink-0 text-fg-faint transition-transform {open ? 'rotate-90' : ''}" />
                        <span class="font-mono text-sm text-fg">{names[d.group]}</span>
                        {#if p}
                          <Tip text={p.blurb || 'Unknown precision'}>
                            <span class="rounded-sm bg-raised px-1.5 text-[11px] whitespace-nowrap text-fg-muted">{precisionShort(p)}</span>
                          </Tip>
                        {/if}
                        {#if best}<Tip text="Largest variant that fits in device memory"><Check size={13} class="text-ok" /></Tip>{/if}
                      </div>
                    </td>
                    <td class="w-full min-w-[10rem]">
                      <div class="flex flex-col gap-1">
                        {#if plan}
                          {#each poolRows(plan) as pool (pool.id)}
                            {@const over = pool.used > pool.cap || plan.verdict === FitVerdict.NO}
                            <div class="flex items-center gap-2 text-[11px] tabular-nums text-fg-faint">
                              <span class="w-7 shrink-0" title={poolName(pool.id)}>{poolWord[pool.kind] ?? 'pool'}</span>
                              <StackBar class="min-w-0 flex-1" legend={false} height="sm" max={pool.cap} segments={[{ label: 'used', value: pool.used, tone: over ? 'bad' : pool.kind === PoolKind.HOST ? 'info' : 'accent' }]} />
                              <span class="w-[5.5rem] shrink-0 text-right whitespace-nowrap {over ? 'text-bad' : ''}">{ratioBytes(pool.used, pool.cap)}</span>
                            </div>
                          {/each}
                        {/if}
                        <div class="flex items-center gap-2 text-[11px] tabular-nums text-fg-faint">
                          <span class="w-7 shrink-0">Disk</span>
                          {#if mount}
                            <StackBar class="min-w-0 flex-1" legend={false} height="sm" max={mount.freeBytes} segments={[{ label: 'weights', value: d.totalBytes, tone: !room ? 'bad' : stored ? 'ok' : 'warn' }]} />
                            <span class="w-[5.5rem] shrink-0 text-right whitespace-nowrap {room ? '' : 'text-bad'}">{ratioStorage(d.totalBytes, mount.freeBytes)}</span>
                          {:else}
                            <span class="flex-1 text-right">{storage(d.totalBytes, 1)}</span>
                          {/if}
                        </div>
                      </div>
                    </td>
                    <td class="actions">
                      <span
                        role="presentation"
                        onclick={(e) => e.stopPropagation()}
                      >
                        {#if task}
                          <TaskChip {task} label="Downloading" />
                        {:else if stored}
                          <Button size="sm" variant={best ? 'primary' : 'secondary'} icon={Play} onclick={() => runModel(stored)}>Run</Button>
                        {:else}
                          <Button size="sm" variant={best ? 'primary' : 'ghost'} icon={Download} onclick={() => pull(d)}>Download</Button>
                        {/if}
                      </span>
                    </td>
                  </tr>
                  {#if open && plan}
                    <tr>
                      <td colspan="3" class="bg-sunken/40 !px-4 !py-3">
                        <PlanTable {plan} />
                      </td>
                    </tr>
                  {/if}
                {/each}
              </tbody>
            </table>
          {:else}
            <Empty compact icon={Files} title="No weight files at this {revLabel}">
              <Button size="sm" onclick={() => (tab = 'files')}>Files</Button>
            </Empty>
          {/if}

          {#if inspect.warnings.length}
            <Disclosure label="Warnings" summary={`${inspect.warnings.length}`} tone="warn" bind:open={showWarnings}>
              <ul class="list-disc pl-5 text-sm leading-6 text-fg-muted">
                {#each inspect.warnings as w, i (i)}<li>{w}</li>{/each}
              </ul>
            </Disclosure>
          {/if}
        </div>
      {/if}
    {:else if shownTab === 'stored'}
      <div class="flex flex-col gap-6">
        {#each storedGroups as m (modelKey(m))}
          {@const serving = servingAs(m)}
          {@const task = taskFor('verify', { source: m.sourceId, repo: m.repo, group: m.group }) ?? taskFor('export', { source: m.sourceId, repo: m.repo, group: m.group })}
          <div class="flex flex-col gap-4 rounded-md border border-line p-4">
            <div class="flex flex-wrap items-center gap-3">
              <span class="font-mono text-sm text-fg">{weightsName(m.group, m.formatId)}</span>
              <span class="text-xs text-fg-faint">{storage(m.bytes)} · downloaded {ago(m.pulledAt, clock.now)}{m.usedAt ? ` · last run ${ago(m.usedAt, clock.now)}` : ''}</span>
              {#each serving as name (name)}<State tone="ok" label="Serving as {name}" />{/each}
              {#if task}<TaskChip {task} />{/if}
              <span class="ml-auto flex items-center gap-1">
                <Button size="sm" variant="primary" icon={Play} onclick={() => runModel(m)}>Run</Button>
                <Button size="sm" variant="ghost" icon={ShieldCheck} onclick={() => verify(m)}>Verify</Button>
                <Button size="sm" variant="ghost" icon={Trash2} class="text-bad hover:text-bad" disabled={serving.length > 0} onclick={() => remove(m)}>Remove</Button>
              </span>
            </div>
            <Kv
              mono
              columns={2}
              items={[
                ['path', m.path],
                ['format', m.formatId],
                ['revision', m.revision || 'default'],
                ['commit', m.commit ? m.commit.slice(0, 12) : undefined],
                ['architecture', m.descriptor?.architecture],
                ['params', m.descriptor?.parameterCount ? fmtParams(m.descriptor.parameterCount) : undefined],
                ['bits per weight', m.descriptor?.bitsPerWeight ? m.descriptor.bitsPerWeight.toFixed(2) : undefined]
              ]}
            />
            <table class="tbl">
              <thead><tr><th>File</th><th>Role</th><th class="num">Size</th><th>Digest</th></tr></thead>
              <tbody>
                {#each m.artifacts as a (a.path)}
                  <tr>
                    <td class="max-w-md truncate font-mono text-xs" title={a.path}>{a.artifact?.path}</td>
                    <td class="text-fg-muted">{enumLabel(ArtifactRole, a.artifact?.role)}</td>
                    <td class="num">{storage(a.artifact?.sizeBytes)}</td>
                    <td class="font-mono text-xs text-fg-faint" title={a.digest}>{a.digest.replace(/^sha256[-:]/, '').slice(0, 12)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
            <form
              class="flex flex-wrap items-center gap-2"
              onsubmit={(e) => {
                e.preventDefault();
                if (exportDir.trim()) exportModel(m);
              }}
            >
              <TextInput class="flex-1" mono bind:value={exportDir} empty="/path/to/mirror" aria-label="Export directory" />
              <Button type="submit" size="md" icon={FolderOutput} loading={exporting === m.group} disabled={!exportDir.trim()}>Export</Button>
            </form>
          </div>
        {/each}
      </div>
    {:else if shownTab === 'files'}
      {#if gated}
        <Empty icon={Lock} title="Gated on {siteName}">
          {#if pageUrl}<Button size="sm" href={pageUrl} icon={ExternalLink}>Accept the license on {siteName}</Button>{/if}
          <span class="text-xs text-fg-faint">Then set {caps?.tokenEnv || 'a token'} for the daemon.</span>
        </Empty>
      {:else if inspecting && !inspect}
        <table class="tbl" aria-busy="true">
          <thead><tr><th>Name</th><th>Role</th><th>Weights</th><th class="num">Size</th><th></th></tr></thead>
          <tbody><SkeletonRows rows={6} cols={['w-48', 'w-16', 'w-20', { w: 'w-14', num: true }, 'w-4']} /></tbody>
        </table>
      {:else if inspectError}
        <Empty compact title={inspectError}>
          <Button size="sm" icon={RefreshCw} onclick={() => load(true)}>Retry</Button>
        </Empty>
      {:else if model}
        {#key curRepo + '\0' + curRev}
          <ModelFiles files={model.artifacts} stored={storedPaths} />
        {/key}
      {/if}
    {:else if shownTab === 'card'}
      {#if card?.markdown || card?.html}
        <Markdown markdown={card.markdown} html={card.html} />
      {:else if cardError}
        <Empty compact title={cardError} />
      {:else if card}
        <Empty compact title="No model card">
          {#if card.url}<Button size="sm" href={card.url} icon={ExternalLink}>Open on {siteName}</Button>{/if}
        </Empty>
      {:else}
        <Skeleton rows={8} />
      {/if}
    {/if}
  </div>
</Drawer>
