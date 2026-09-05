<script lang="ts">
  import { untrack } from 'svelte';
  import { Code } from '@connectrpc/connect';
  import { api, code, message } from '$lib/api';
  import { live, clock, taskFor, modelKey, profilesOf, formatBlurb, hostName, runtimeName } from '$lib/state.svelte';
  import { runModel } from '$lib/slotActions.svelte';
  import { ago, bytes, count, params as fmtParams, when, enumLabel, byName, verdictWord } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { descriptorKey, facetValueLabel, fitSummary, hitSize, locked, orderDescriptors, precisionTone, runtimesFor } from '$lib/catalog';
  import { FitVerdict } from '$proto/estimate_pb';
  import type { SearchHit, SourceCapabilities, Revision, ModelCard } from '$proto/source_pb';
  import type { RuntimeStatus } from '$proto/runtime_pb';
  import type { InspectResponse } from '$proto/estimate_pb';
  import { ArtifactRole } from '$proto/model_pb';
  import { Download, Eye, Play, Check, ExternalLink, ArrowDownToLine, Heart, RefreshCw, Lock } from '@lucide/svelte';
  import Drawer from './ui/Drawer.svelte';
  import Tabs from './ui/Tabs.svelte';
  import Segmented from './ui/Segmented.svelte';
  import Select from './ui/Select.svelte';
  import Button from './ui/Button.svelte';
  import Skeleton from './ui/Skeleton.svelte';
  import SkeletonRows from './ui/SkeletonRows.svelte';
  import Copy from './ui/Copy.svelte';
  import Empty from './ui/Empty.svelte';
  import Markdown from './ui/Markdown.svelte';
  import Tip from './ui/Tip.svelte';
  import State from './ui/State.svelte';
  import Section from './ui/Section.svelte';
  import Disclosure from './ui/Disclosure.svelte';
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
  // A gated repository on a source without a token cannot be read, and a refusal says the same
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

  // Plans again when the slot or profile changes, the listing being cached daemon side
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
  function serves(formatId: string): boolean {
    return runtimesFor([formatId], runtimes).length > 0;
  }
  const bars: Record<string, string> = { ok: 'bg-ok', accent: 'bg-accent', warn: 'bg-warn', bad: 'bg-bad', neutral: 'bg-fg-faint', info: 'bg-info' };
</script>

{#snippet filesTable(files: typeof otherFiles)}
  <table class="tbl">
    <thead><tr><th>Path</th><th>Role</th><th>Format</th><th class="num">Size</th></tr></thead>
    <tbody>
      {#each files as a (a.path)}
        <tr>
          <td class="max-w-md truncate font-mono text-xs" title={a.path}>{a.path}</td>
          <td class="text-fg-muted">{enumLabel(ArtifactRole, a.role)}</td>
          <td class="text-fg-muted">{a.formatId || '–'}</td>
          <td class="num">{bytes(a.sizeBytes)}</td>
        </tr>
      {/each}
    </tbody>
  </table>
{/snippet}

<Drawer bind:open mono title={model?.repo || curRepo || 'Model'} subtitle={hit?.author ? `${hit.author}${hit.name && hit.name !== hit.repo ? ' · ' + hit.name : ''}` : siteName}>
  {#snippet header()}
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-fg-muted">
      {#if hit?.task}<span class="text-fg">{facetValueLabel(caps, 'task', hit.task)}</span>{/if}
      {#if hit && hit.downloads > 0n}<span class="inline-flex items-center gap-1 tabular-nums"><ArrowDownToLine size={12} />{count(hit.downloads)}</span>{/if}
      {#if hit && hit.likes > 0n}<span class="inline-flex items-center gap-1 tabular-nums"><Heart size={12} />{count(hit.likes)}</span>{/if}
      {#if size.kind === 'params'}<span>{fmtParams(size.value)} params</span>{:else if size.kind === 'bytes'}<span>{bytes(size.value, 1)}</span>{/if}
      {#if hit?.license}<span>{hit.license}</span>{/if}
      {#if hit?.updatedAt}<span>{ago(hit.updatedAt, clock.now)}</span>{/if}
      {#if pageUrl}<a href={pageUrl} target="_blank" rel="noopener noreferrer" class="link inline-flex items-center gap-1"><ExternalLink size={12} />{siteName}</a>{/if}
    </div>
    <div class="mt-3 flex flex-wrap items-end gap-3">
      <Tabs size="sm" bind:value={tab} {tabs} class="flex-1" />
      {#if revisions.length > 1}
        <Select
          size="sm"
          mono
          class="w-auto max-w-[14rem]"
          label={revLabel}
          value={currentRevision?.name ?? ''}
          items={revisions.map((r) => ({ value: r.name, label: r.name, detail: r.sizeBytes ? bytes(r.sizeBytes, 1) : undefined }))}
        />
      {/if}
    </div>
  {/snippet}

  <div class="px-6 py-5">
    {#if tab === 'weights'}
      {#if gated}
        <Empty icon={Lock} title="Gated on {siteName}">
          {#if pageUrl}<Button size="sm" href={pageUrl} icon={ExternalLink}>Accept the license</Button>{/if}
          <Button size="sm" variant="ghost" href="/settings">Set {caps?.tokenEnv || 'a token'}</Button>
        </Empty>
      {:else if inspecting}
        <table class="tbl" aria-busy="true">
          <thead><tr><th>Weights</th><th>Precision</th><th class="num">Size</th><th class="num">Params</th><th>Fit</th><th></th></tr></thead>
          <tbody><SkeletonRows rows={3} cols={[{ w: 'w-24', sub: true }, 'w-20', { w: 'w-14', num: true }, { w: 'w-10', num: true }, 'w-24', { w: 'w-12', num: true }]} /></tbody>
        </table>
      {:else if inspectError}
        <Empty compact title={inspectError}>
          <Button size="sm" icon={RefreshCw} onclick={load}>Retry</Button>
        </Empty>
      {:else if inspect && model}
        <div class="flex flex-col gap-7">
          <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-fg-faint">
            <span>{revLabel} <span class="font-mono text-fg-muted">{model.revision || 'default'}</span></span>
            {#if model.commit}<span class="font-mono text-fg-muted" title={model.commit}>{model.commit.slice(0, 12)}</span>{/if}
            <span>{model.artifacts.length} files</span>
            <span>{when(model.resolvedAt)}</span>
            <span class="ml-auto"><Copy text={model.repo} size={13} label="Copy repository" /></span>
          </div>

          <Section title="Weights" count={ordered.length || undefined}>
            {#snippet actions()}
              {#if slots.length}
                <Segmented size="sm" bind:value={slotId} tabs={[{ id: '', label: hostName() || 'host' }, ...slots.map((s) => ({ id: s.id, label: s.name }))]} />
              {/if}
              {#if profiles.length}
                <Select size="sm" class="w-auto" bind:value={profileId} label="Profile" items={[{ value: '', label: 'Default profiles' }, ...profiles.map((p) => ({ value: p.id, label: p.name, detail: p.runtimeId }))]} />
              {/if}
            {/snippet}
            {#if ordered.length}
              <table class="tbl">
                <thead><tr><th>Weights</th><th>Precision</th><th class="num">Size</th><th class="num">Params</th><th>Fit</th><th></th></tr></thead>
                <tbody>
                  {#each ordered as d (descriptorKey(d))}
                    {@const stored = storedModel(d.group)}
                    {@const task = pulling(d.group)}
                    {@const p = d.precision}
                    {@const tone = precisionTone(p?.level ?? 0)}
                    {@const fit = fitSummary(inspect.rows, d.group)}
                    {@const now = fitSummary(inspect.rows, d.group, true)}
                    {@const best = d.group === bestGroup && ordered.length > 1}
                    <tr>
                      <td>
                        <div class="flex items-center gap-2">
                          <span class="font-mono text-sm text-fg">{d.group}</span>
                          {#if best}<Tip text="The largest that fits"><Check size={13} class="text-ok" /></Tip>{/if}
                        </div>
                        <div class="mt-0.5 truncate text-xs text-fg-faint"><span title={formatBlurb(d.formatId)}>{d.formatId}</span>{#if d.architecture}<span>{' · '}{d.architecture}</span>{/if}</div>
                      </td>
                      <td>
                        <Tip text={p?.blurb || 'Unknown precision'}>
                          <span class="flex cursor-help items-center gap-2">
                            <span class="flex items-center gap-0.5">
                              {#each [1, 2, 3, 4, 5] as i (i)}
                                <span class="h-2.5 w-1 rounded-sm {i <= (p?.level ?? 0) ? bars[tone] : 'bg-line'}"></span>
                              {/each}
                            </span>
                            <span class="text-xs whitespace-nowrap text-fg-muted">{p?.label ?? '–'}</span>
                          </span>
                        </Tip>
                      </td>
                      <td class="num">{bytes(d.totalBytes, 1)}</td>
                      <td class="num text-fg-muted">{fmtParams(d.parameterCount)}</td>
                      <td>
                        {#if fit}
                          <State tone={fit.tone} label={fit.label} />
                          {#if now && (now.verdict !== fit.verdict || now.context !== fit.context)}<div class="text-xs {now.tone === 'bad' ? 'text-bad' : now.tone === 'warn' ? 'text-warn' : 'text-fg-faint'}">now {(now.verdict === FitVerdict.FITS ? now.label : verdictWord(now.verdict)).toLowerCase()}</div>{/if}
                          {#if planRuntimes.length > 1}<div class="text-xs text-fg-faint">{runtimeName(fit.runtime)}</div>{/if}
                        {:else if serves(d.formatId)}
                          <span class="text-xs text-fg-faint">Not planned</span>
                        {:else}
                          <span class="text-xs text-fg-faint">No runtime for {d.formatId}</span>
                        {/if}
                      </td>
                      <td class="actions">
                        <span>
                          {#if task}
                            <TaskChip {task} label="Pulling" />
                          {:else if stored}
                            <Button size="sm" variant="primary" icon={Play} onclick={() => runModel(stored)}>Run</Button>
                          {:else}
                            <Button size="sm" variant="primary" icon={Download} onclick={() => pull(d.group)}>Pull</Button>
                          {/if}
                        </span>
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            {:else}
              {@render filesTable(model.artifacts)}
            {/if}
          </Section>

          {#if inspect.rows.length}
            <Section title="Fit by context" info="Longer context needs more cache memory. A cell opens its plan.">
              <FitTable rows={inspect.rows} />
            </Section>
          {/if}

          {#if inspect.warnings.length}
            <Disclosure label="Warnings" summary={String(inspect.warnings.length)} tone="warn" bind:open={showWarnings}>
              <ul class="list-disc pl-5 text-sm leading-6 text-fg-muted">
                {#each inspect.warnings as w, i (i)}<li>{w}</li>{/each}
              </ul>
            </Disclosure>
          {/if}

          {#if ordered.length && otherFiles.length}
            <Disclosure label="Other files" summary={String(otherFiles.length)} bind:open={showFiles}>
              {@render filesTable(otherFiles)}
            </Disclosure>
          {/if}
        </div>
      {/if}
    {:else if tab === 'card'}
      {#if card?.markdown || card?.html}
        <Markdown markdown={card.markdown} html={card.html} />
      {:else if cardError}
        <Empty compact title={cardError} />
      {:else if card}
        <Empty compact title="No card">
          {#if card.url}<Button size="sm" href={card.url} icon={ExternalLink}>Open on {siteName}</Button>{/if}
        </Empty>
      {:else}
        <Skeleton rows={8} />
      {/if}
    {:else if tab === 'revisions'}
      <table class="tbl">
        <thead><tr><th>{revLabel}</th><th>Detail</th><th class="num">Size</th><th>Updated</th><th>Commit</th><th></th></tr></thead>
        <tbody>
          {#each revisions as r (r.repo || r.name)}
            {@const current = r === currentRevision}
            <tr class={current ? 'row-active' : ''}>
              <td class="font-mono text-xs text-fg">{r.name}{#if r.default}<span class="ml-1.5 font-sans text-xs text-fg-faint">default</span>{/if}</td>
              <td class="max-w-xs truncate text-fg-muted" title={r.detail}>{r.detail || '–'}</td>
              <td class="num">{r.sizeBytes ? bytes(r.sizeBytes) : '–'}</td>
              <td class="text-fg-muted">{r.updatedAt ? ago(r.updatedAt, clock.now) : '–'}</td>
              <td class="font-mono text-xs text-fg-faint">{r.commit ? r.commit.slice(0, 12) : '–'}</td>
              <td class="actions"><span>{#if current}<span class="text-xs text-accent">viewing</span>{:else}<Button size="sm" variant="ghost" onclick={() => pick(r)}>View</Button>{/if}</span></td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </div>

  {#snippet footer()}
    <div class="ml-auto flex gap-2">
      <Button size="sm" variant={watched ? 'ghost' : 'secondary'} icon={watched ? Check : Eye} disabled={watched || gated} onclick={() => (watchOpen = true)}>{watched ? 'Watching' : 'Watch'}</Button>
    </div>
  {/snippet}
</Drawer>

<WatchDialog bind:open={watchOpen} {sourceId} repo={model?.repo ?? curRepo} />
