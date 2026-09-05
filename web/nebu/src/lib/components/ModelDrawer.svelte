<script lang="ts">
  import { untrack } from 'svelte';
  import { Code, ConnectError } from '@connectrpc/connect';
  import { api, message } from '$lib/api';
  import { live, clock, taskFor, modelKey, profilesOf, formatBlurb, hostName } from '$lib/state.svelte';
  import { runModel } from '$lib/slotActions.svelte';
  import { ago, bytes, count, params as fmtParams, when, enumLabel, byName } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { facetValueLabel, fitSummary, hitSize, locked, orderDescriptors, precisionTone, runtimesFor } from '$lib/catalog';
  import { FitVerdict } from '$proto/estimate_pb';
  import type { SearchHit, SourceCapabilities, Revision, ModelCard } from '$proto/source_pb';
  import type { RuntimeStatus } from '$proto/runtime_pb';
  import type { InspectResponse } from '$proto/estimate_pb';
  import { ArtifactRole } from '$proto/model_pb';
  import { Download, Eye, Play, Check, ExternalLink, ArrowDownToLine, Heart, RefreshCw, ChevronDown, Lock } from '@lucide/svelte';
  import Drawer from './ui/Drawer.svelte';
  import Tabs from './ui/Tabs.svelte';
  import Button from './ui/Button.svelte';
  import Skeleton from './ui/Skeleton.svelte';
  import Copy from './ui/Copy.svelte';
  import Empty from './ui/Empty.svelte';
  import Markdown from './ui/Markdown.svelte';
  import Tip from './ui/Tip.svelte';
  import Info from './ui/Info.svelte';
  import FitTable from './FitTable.svelte';
  import TaskChip from './TaskChip.svelte';
  import WatchDialog from './WatchDialog.svelte';

  let {
    open = $bindable(false),
    sourceId,
    sourceLabel = '',
    repo,
    revision = '',
    caps,
    runtimes = [],
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
    runtimes?: RuntimeStatus[];
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
  let watchOpen = $state(false);
  let showFiles = $state(false);
  let showWarnings = $state(false);
  let profileId = $state('');
  let generation = 0;

  const watched = $derived(!!curRepo && [...live.watches.values()].some((w) => w.sourceId === sourceId && w.repo === curRepo));
  const model = $derived(inspect?.model);
  const revLabel = $derived(caps?.revisionLabel || 'revision');
  const currentRevision = $derived(revisions.find((r) => (r.repo ? r.repo === (model?.repo ?? curRepo) : r.name === (model?.revision ?? curRev))) ?? revisions.find((r) => r.default));
  const otherFiles = $derived((model?.artifacts ?? []).filter((a) => a.role !== ArtifactRole.WEIGHTS));
  const size = $derived(hit ? hitSize(hit) : { kind: 'none' as const, value: 0n });
  const pageUrl = $derived(hit?.url || card?.url || '');
  const siteName = $derived(sourceLabel || caps?.name || 'the source');
  const planRuntimes = $derived([...new Set((inspect?.rows ?? []).map((r) => r.runtimeId))]);
  const slots = $derived([...live.slots.values()].sort(byName((s) => s.name)));
  const profiles = $derived(profilesOf(''));
  const ordered = $derived(inspect ? orderDescriptors(inspect.descriptors, inspect.rows) : []);
  // The first row that fits is the largest that does, the usual thing to pull
  const bestGroup = $derived(ordered.find((d) => fitSummary(inspect?.rows ?? [], d.group)?.verdict === FitVerdict.FITS)?.group ?? '');
  // A gated repository on a source without a token cannot be read, so nothing is asked of it, and a refusal says the same
  const gated = $derived(locked(hit, caps) || denied);
  const tabs = $derived([{ id: 'weights', label: 'Weights' }, ...(caps?.card ? [{ id: 'card', label: 'Card' }] : []), ...(revisions.length > 1 ? [{ id: 'revisions', label: revLabel.charAt(0).toUpperCase() + revLabel.slice(1) + 's', count: revisions.length }] : [])]);

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
      load();
    });
  });

  async function load() {
    const gen = ++generation;
    tab = gated && caps?.card ? 'card' : 'weights';
    inspect = null;
    inspectError = '';
    denied = false;
    revisions = [];
    card = null;
    cardError = '';
    showFiles = false;
    showWarnings = false;
    planned = planKey();
    const target = { sourceId, repo: curRepo, revision: curRev };
    const jobs: Promise<void>[] = [];
    if (!gated) {
      inspecting = true;
      jobs.push(
        api.estimate
          .inspect({ ...target, slotId, profileId })
          .then((r) => {
            if (gen === generation) inspect = r;
          })
          .catch((err) => {
            if (gen !== generation) return;
            if (err instanceof ConnectError && err.code === Code.PermissionDenied) denied = true;
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

  // Re-plans when the slot or profile changes, the listing is cached daemon side so it is cheap
  let planned = '';
  function planKey() {
    return `${slotId}\0${profileId}`;
  }
  $effect(() => {
    const key = planKey();
    untrack(() => {
      if (key === planned) return;
      planned = key;
      if (open && inspect && !inspecting) refit();
    });
  });
  async function refit() {
    const gen = generation;
    try {
      const r = await api.estimate.inspect({ sourceId, repo: curRepo, revision: curRev, slotId, profileId });
      if (gen === generation) inspect = r;
    } catch (err) {
      if (gen === generation) inspectError = message(err);
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
    load();
  }

  async function pull(group: string) {
    if (!model) return;
    try {
      const r = await api.store.pull({ sourceId, repo: model.repo, revision: model.revision, group });
      ok(`Pulling ${model.repo}`, group, r.task ? { href: `/tasks?id=${r.task.id}`, label: 'Task' } : undefined);
    } catch (err) {
      fail(err, 'Pull refused');
    }
  }
  function storedModel(group: string) {
    return model ? live.models.get(modelKey({ sourceId, repo: model.repo, group })) : undefined;
  }
  function pulling(group: string) {
    return model ? taskFor('pull', { source: sourceId, repo: model.repo, group }) : undefined;
  }
  function runtimeNames(formatId: string): string {
    return runtimesFor([formatId], runtimes)
      .map((r) => r.name)
      .join(', ');
  }
  const bars: Record<string, string> = { ok: 'bg-ok', accent: 'bg-accent', warn: 'bg-warn', bad: 'bg-bad', neutral: 'bg-fg-faint', info: 'bg-info' };
</script>

{#snippet filesTable(files: typeof otherFiles)}
  <div class="overflow-x-auto rounded-lg border border-line">
    <table class="tbl">
      <thead><tr><th>path</th><th>role</th><th>format</th><th class="num">size</th></tr></thead>
      <tbody>
        {#each files as a (a.path)}
          <tr>
            <td class="max-w-md truncate font-mono text-xs" title={a.path}>{a.path}</td>
            <td class="text-xs text-fg-muted">{enumLabel(ArtifactRole, a.role)}</td>
            <td class="text-xs text-fg-muted">{a.formatId || '–'}</td>
            <td class="num text-xs">{bytes(a.sizeBytes)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/snippet}

<Drawer bind:open title={model?.repo || curRepo || 'Model'} subtitle={hit?.author ? `${hit.author}${hit.name && hit.name !== hit.repo ? ' · ' + hit.name : ''}` : siteName}>
  {#snippet header()}
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-fg-muted">
      {#if hit?.task}<span class="text-fg">{facetValueLabel(caps, 'task', hit.task)}</span>{/if}
      {#if hit && hit.downloads > 0n}<span class="inline-flex items-center gap-1 tabular-nums"><ArrowDownToLine size={11} />{count(hit.downloads)}</span>{/if}
      {#if hit && hit.likes > 0n}<span class="inline-flex items-center gap-1 tabular-nums"><Heart size={11} />{count(hit.likes)}</span>{/if}
      {#if size.kind === 'params'}<span>{fmtParams(size.value)} params</span>{:else if size.kind === 'bytes'}<span>{bytes(size.value, 1)}</span>{/if}
      {#if hit?.license}<span>{hit.license}</span>{/if}
      {#if hit?.updatedAt}<span>{ago(hit.updatedAt, clock.now)}</span>{/if}
      {#if pageUrl}<a href={pageUrl} target="_blank" rel="noopener noreferrer" class="inline-flex items-center gap-1 text-accent hover:underline"><ExternalLink size={11} />{siteName}</a>{/if}
    </div>
    <div class="mt-3 flex flex-wrap items-center gap-2">
      <Tabs size="sm" bind:value={tab} {tabs} />
      {#if revisions.length > 1}
        <select class="input ml-auto h-7 w-auto max-w-[14rem] py-0 pr-7 text-xs" value={currentRevision?.name ?? ''} onchange={(e) => { const r = revisions.find((x) => x.name === (e.currentTarget as HTMLSelectElement).value); if (r) pick(r); }} aria-label={revLabel}>
          {#each revisions as r (r.repo || r.name)}
            <option value={r.name}>{r.name}{r.sizeBytes ? ` · ${bytes(r.sizeBytes, 1)}` : ''}</option>
          {/each}
        </select>
      {/if}
    </div>
  {/snippet}

  <div class="px-5 py-4">
    {#if tab === 'weights'}
      {#if gated}
        <Empty icon={Lock} title="Gated" description="Accept the license on {siteName} and set {caps?.tokenEnv || 'a token'} for the daemon">
          {#if pageUrl}<Button size="sm" href={pageUrl} icon={ExternalLink}>Open on {siteName}</Button>{/if}
          <Button size="sm" variant="ghost" href="/settings">Sources</Button>
        </Empty>
      {:else if inspecting}
        <div class="overflow-hidden rounded-lg border border-line">
          <table class="tbl">
            <thead><tr><th>Weights</th><th>Precision</th><th class="num">Params</th><th class="num">Size</th><th>Fit</th><th></th></tr></thead>
            <tbody>
              {#each [0, 1, 2, 3] as i (i)}
                <tr aria-busy="true">
                  <td><div class="skeleton h-3.5" style="width: {50 + i * 12}%"></div><div class="skeleton mt-1.5 h-2.5 w-1/3"></div></td>
                  <td><div class="skeleton h-3 w-20"></div></td>
                  <td class="num"><div class="skeleton ml-auto h-3 w-10"></div></td>
                  <td class="num"><div class="skeleton ml-auto h-3 w-14"></div></td>
                  <td><div class="skeleton h-3 w-24"></div></td>
                  <td><div class="skeleton ml-auto h-6 w-14"></div></td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {:else if inspectError}
        <Empty compact title="Could not read {curRepo}" description={inspectError}>
          <Button size="sm" icon={RefreshCw} onclick={load}>Retry</Button>
        </Empty>
      {:else if inspect && model}
        <div class="flex flex-col gap-6">
          <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-fg-faint">
            <span>{revLabel} <span class="font-mono text-fg-muted">{model.revision || 'default'}</span></span>
            {#if model.commit}<span class="font-mono text-fg-muted" title={model.commit}>{model.commit.slice(0, 12)}</span>{/if}
            <span>{model.artifacts.length} files</span>
            <span>{when(model.resolvedAt)}</span>
            <span class="ml-auto inline-flex items-center gap-1"><Copy text={model.repo} size={12} /></span>
          </div>

          {#if slots.length || profiles.length}
            <div class="flex flex-wrap items-center gap-2 text-xs">
              {#if slots.length}
                <span class="text-fg-faint">Plan for</span>
                <Tabs size="sm" bind:value={slotId} tabs={[{ id: '', label: hostName() || 'this host' }, ...slots.map((s) => ({ id: s.id, label: s.name }))]} />
              {/if}
              {#if profiles.length}
                <select class="input h-7 w-auto py-0 pr-7 text-xs {slots.length ? 'ml-auto' : ''}" bind:value={profileId} aria-label="Profile">
                  <option value="">Default profiles</option>
                  {#each profiles as p (p.id)}<option value={p.id}>{p.runtimeId} · {p.name}</option>{/each}
                </select>
              {/if}
            </div>
          {/if}

          {#if ordered.length}
            <div class="overflow-x-auto rounded-lg border border-line">
              <table class="tbl">
                <thead>
                  <tr>
                    <th>Weights</th>
                    <th><Tip text="Bits per weight. Fewer bits means smaller and less accurate"><span class="cursor-help uppercase">Precision</span></Tip></th>
                    <th class="num">Params</th>
                    <th class="num">Size</th>
                    <th><Tip text="Whether weights and cache fit device memory at the longest context planned. Now is against what is free at this moment"><span class="cursor-help uppercase">Fit</span></Tip></th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {#each ordered as d (d.group)}
                    {@const stored = storedModel(d.group)}
                    {@const task = pulling(d.group)}
                    {@const p = d.precision}
                    {@const tone = precisionTone(p?.level ?? 0)}
                    {@const fit = fitSummary(inspect.rows, d.group)}
                    {@const now = fitSummary(inspect.rows, d.group, true)}
                    {@const runners = runtimeNames(d.formatId)}
                    {@const best = d.group === bestGroup && ordered.length > 1}
                    <tr class={best ? 'bg-ok/5' : ''}>
                      <td class="border-l-2 {best ? 'border-ok' : 'border-transparent'}">
                        <div class="font-mono text-xs text-fg">{d.group}</div>
                        <div class="mt-0.5 text-[11px] text-fg-faint"><span class="font-mono" title={formatBlurb(d.formatId)}>{d.formatId}</span>{#if d.architecture}<span> · {d.architecture}</span>{/if}</div>
                      </td>
                      <td>
                        <Tip text={p?.blurb || 'Unknown'}>
                          <span class="flex cursor-help items-center gap-2">
                            <span class="flex items-center gap-0.5">
                              {#each [1, 2, 3, 4, 5] as i (i)}
                                <span class="h-2 w-1.5 rounded-sm {i <= (p?.level ?? 0) ? bars[tone] : 'bg-line'}"></span>
                              {/each}
                            </span>
                            <span class="text-xs whitespace-nowrap text-fg">{p?.label}</span>
                          </span>
                        </Tip>
                      </td>
                      <td class="num text-xs">{fmtParams(d.parameterCount)}</td>
                      <td class="num text-xs whitespace-nowrap">{bytes(d.totalBytes, 1)}</td>
                      <td>
                        {#if fit}
                          <div class="text-xs whitespace-nowrap {fit.tone === 'ok' ? 'text-ok' : fit.tone === 'warn' ? 'text-warn' : 'text-bad'}">{fit.label}</div>
                          {#if now && (now.verdict !== fit.verdict || now.context !== fit.context)}<div class="text-[11px] {now.tone === 'bad' ? 'text-bad' : now.tone === 'warn' ? 'text-warn' : 'text-fg-faint'}">now {now.label.toLowerCase()}</div>{/if}
                          {#if planRuntimes.length > 1}<div class="text-[11px] text-fg-faint">{fit.runtime}</div>{/if}
                        {:else if runners}
                          <span class="text-xs text-fg-faint">Not planned</span>
                        {:else}
                          <span class="text-xs text-fg-faint">No runtime for {d.formatId}</span>
                        {/if}
                      </td>
                      <td class="text-right whitespace-nowrap">
                        {#if task}
                          <TaskChip {task} label="Pulling" />
                        {:else if stored}
                          <Button size="xs" variant="primary" icon={Play} onclick={() => runModel(stored)}>Run</Button>
                        {:else}
                          <Button size="xs" variant="primary" icon={Download} onclick={() => pull(d.group)}>Pull</Button>
                        {/if}
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          {/if}

          {#if inspect.rows.length}
            <section>
              <div class="mb-2 flex items-center gap-2">
                <h3 class="text-[11px] font-semibold tracking-wider text-fg-faint uppercase">Fit by context</h3>
                <Info text="Context is the prompt plus the reply in tokens. Longer context needs more cache memory. Click a cell for the plan" class="ml-auto" />
              </div>
              <FitTable rows={inspect.rows} />
            </section>
          {/if}

          {#if inspect.warnings.length}
            <section>
              <button class="flex items-center gap-1.5 text-[11px] font-semibold tracking-wider text-warn uppercase" onclick={() => (showWarnings = !showWarnings)}>
                <ChevronDown size={12} class="transition-transform {showWarnings ? 'rotate-180' : ''}" />
                {inspect.warnings.length} {inspect.warnings.length === 1 ? 'warning' : 'warnings'}
              </button>
              {#if showWarnings}
                <ul class="mt-2 rounded-lg border border-warn/30 bg-warn/8 px-3 py-2 text-xs leading-5 text-warn">
                  {#each inspect.warnings as w, i (i)}<li>{w}</li>{/each}
                </ul>
              {/if}
            </section>
          {/if}

          {#if ordered.length === 0}
            {@render filesTable(model.artifacts)}
          {:else if otherFiles.length}
            <section>
              <button class="flex items-center gap-1.5 text-[11px] font-semibold tracking-wider text-fg-faint uppercase hover:text-fg" onclick={() => (showFiles = !showFiles)}>
                <ChevronDown size={12} class="transition-transform {showFiles ? 'rotate-180' : ''}" />
                Other files ({otherFiles.length})
              </button>
              {#if showFiles}<div class="mt-2">{@render filesTable(otherFiles)}</div>{/if}
            </section>
          {/if}
        </div>
      {/if}
    {:else if tab === 'card'}
      {#if card?.markdown || card?.html}
        <Markdown markdown={card.markdown} html={card.html} />
      {:else if cardError}
        <Empty compact title="No card" description={cardError} />
      {:else if card}
        <Empty compact title="No card">
          {#if card.url}<Button size="sm" href={card.url} icon={ExternalLink}>Open on {siteName}</Button>{/if}
        </Empty>
      {:else}
        <Skeleton rows={8} />
      {/if}
    {:else if tab === 'revisions'}
      <div class="overflow-x-auto rounded-lg border border-line">
        <table class="tbl">
          <thead><tr><th>{revLabel}</th><th>detail</th><th class="num">size</th><th>updated</th><th>commit</th><th></th></tr></thead>
          <tbody>
            {#each revisions as r (r.repo || r.name)}
              {@const current = r === currentRevision}
              <tr class={current ? 'row-active' : ''}>
                <td class="font-mono text-xs text-fg">{r.name}{#if r.default}<span class="ml-1.5 text-[10.5px] text-fg-faint">default</span>{/if}</td>
                <td class="max-w-xs truncate text-xs text-fg-muted" title={r.detail}>{r.detail || '–'}</td>
                <td class="num text-xs">{r.sizeBytes ? bytes(r.sizeBytes) : '–'}</td>
                <td class="text-xs text-fg-muted">{r.updatedAt ? ago(r.updatedAt, clock.now) : '–'}</td>
                <td class="font-mono text-xs text-fg-faint">{r.commit ? r.commit.slice(0, 12) : '–'}</td>
                <td class="text-right">{#if current}<span class="text-[11px] text-accent">viewing</span>{:else}<Button size="xs" variant="ghost" onclick={() => pick(r)}>View</Button>{/if}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </div>

  {#snippet footer()}
    <div class="ml-auto flex gap-2">
      <Button size="sm" variant={watched ? 'ghost' : 'outline'} icon={watched ? Check : Eye} disabled={watched || gated} onclick={() => (watchOpen = true)}>{watched ? 'Watching' : 'Watch'}</Button>
    </div>
  {/snippet}
</Drawer>

<WatchDialog bind:open={watchOpen} {sourceId} repo={model?.repo ?? curRepo} />
