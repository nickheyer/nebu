<script lang="ts">
  import { untrack } from 'svelte';
  import { api, message } from '$lib/api';
  import { live, clock, taskFor, modelKey } from '$lib/state.svelte';
  import { runModel } from '$lib/slotActions.svelte';
  import { ago, bytes, count, params as fmtParams, when, enumLabel } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { facetValueLabel, fitSummary, formatBlurb, hitSize, orderDescriptors, precision, runtimesFor } from '$lib/catalog';
  import { FitVerdict } from '$proto/estimate_pb';
  import type { SearchHit, SourceCapabilities, Revision, ModelCard } from '$proto/source_pb';
  import type { RuntimeStatus } from '$proto/runtime_pb';
  import type { InspectResponse } from '$proto/estimate_pb';
  import { ArtifactRole } from '$proto/model_pb';
  import { Download, Eye, Play, Check, ExternalLink, ArrowDownToLine, Heart, GitBranch, RefreshCw, ChevronDown, HelpCircle } from '@lucide/svelte';
  import Drawer from './ui/Drawer.svelte';
  import Tabs from './ui/Tabs.svelte';
  import Button from './ui/Button.svelte';
  import Badge from './ui/Badge.svelte';
  import Skeleton from './ui/Skeleton.svelte';
  import Copy from './ui/Copy.svelte';
  import Empty from './ui/Empty.svelte';
  import Markdown from './ui/Markdown.svelte';
  import Tip from './ui/Tip.svelte';
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
  let tab = $state('files');
  let inspect = $state<InspectResponse | null>(null);
  let inspectError = $state('');
  let inspecting = $state(false);
  let revisions = $state<Revision[]>([]);
  let card = $state<ModelCard | null>(null);
  let cardError = $state('');
  let watchOpen = $state(false);
  let showFiles = $state(false);
  let generation = 0;

  const watched = $derived(!!curRepo && [...live.watches.values()].some((w) => w.sourceId === sourceId && w.repo === curRepo));
  const model = $derived(inspect?.model);
  const revLabel = $derived(caps?.revisionLabel || 'revision');
  const currentRevision = $derived(revisions.find((r) => (r.repo ? r.repo === (model?.repo ?? curRepo) : r.name === (model?.revision ?? curRev))) ?? revisions.find((r) => r.default));
  const otherFiles = $derived((model?.artifacts ?? []).filter((a) => a.role !== ArtifactRole.WEIGHTS));
  const size = $derived(hit ? hitSize(hit) : { kind: 'none' as const, value: 0n });
  const pageUrl = $derived(hit?.url || card?.url || '');
  const siteName = $derived(sourceLabel || caps?.description?.split(',')[0] || 'the source');
  const planRuntimes = $derived([...new Set((inspect?.rows ?? []).map((r) => r.runtimeId))]);
  const slotName = $derived(slotId ? (live.slots.get(slotId)?.name ?? 'the slot') : '');
  const descriptorTitle = $derived(inspect && inspect.descriptors.length > 1 ? `${inspect.descriptors.length} versions of the weights` : 'The weights');
  const ordered = $derived(inspect ? orderDescriptors(inspect.descriptors, inspect.rows) : []);
  // The first row that fits is the largest that does, the usual thing to pull
  const bestGroup = $derived(ordered.find((d) => fitSummary(inspect?.rows ?? [], d.group)?.verdict === FitVerdict.FITS)?.group ?? '');

  // A new target resets everything, the same target keeps what is loaded
  $effect(() => {
    const o = open;
    const r = repo;
    const v = revision;
    untrack(() => {
      if (!o) return;
      if (r === curRepo && v === curRev && (inspect || inspecting)) return;
      curRepo = r;
      curRev = v;
      load();
    });
  });

  async function load() {
    const gen = ++generation;
    tab = 'files';
    inspect = null;
    inspectError = '';
    revisions = [];
    card = null;
    cardError = '';
    showFiles = false;
    inspecting = true;
    plannedSlot = slotId;
    const target = { sourceId, repo: curRepo, revision: curRev };
    const jobs: Promise<void>[] = [
      api.estimate
        .inspect({ ...target, slotId })
        .then((r) => {
          if (gen === generation) inspect = r;
        })
        .catch((err) => {
          if (gen === generation) inspectError = message(err);
        })
        .finally(() => {
          if (gen === generation) inspecting = false;
        })
    ];
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

  // Re-plans when the slot changes, the listing is cached daemon side so it is cheap
  let plannedSlot = '';
  $effect(() => {
    const s = slotId;
    untrack(() => {
      if (s === plannedSlot) return;
      plannedSlot = s;
      if (open && inspect && !inspecting) refit();
    });
  });
  async function refit() {
    const gen = generation;
    try {
      const r = await api.estimate.inspect({ sourceId, repo: curRepo, revision: curRev, slotId });
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
      ok(`Pulling ${model.repo}`, group, r.task ? { href: `/tasks?id=${r.task.id}`, label: 'Follow the pull' } : undefined);
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
</script>

{#snippet th(label: string, help: string, num = false)}
  <th class={num ? 'num' : ''}>
    <Tip text={help}>
      <span class="inline-flex cursor-help items-center gap-1 uppercase">{label}<HelpCircle size={10} class="text-fg-faint/70" /></span>
    </Tip>
  </th>
{/snippet}

<Drawer bind:open title={model?.repo || curRepo || 'Model'} subtitle={hit?.author ? `by ${hit.author}${hit.name && hit.name !== hit.repo ? ' · ' + hit.name : ''}` : `on ${siteName}`} width="2xl">
  {#snippet header()}
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-fg-muted">
      {#if hit?.task}<span class="rounded-md bg-accent/12 px-1.5 py-0.5 text-[11px] font-medium text-accent" title="What the model is for">{facetValueLabel(caps, 'task', hit.task)}</span>{/if}
      {#if hit && hit.downloads > 0n}<span class="inline-flex items-center gap-1 tabular-nums" title="Downloads counted by the source"><ArrowDownToLine size={11} />{count(hit.downloads)} downloads</span>{/if}
      {#if hit && hit.likes > 0n}<span class="inline-flex items-center gap-1 tabular-nums" title="Likes on the source"><Heart size={11} />{count(hit.likes)} likes</span>{/if}
      {#if size.kind === 'params'}<span title="Parameter count. More parameters usually means smarter answers and more memory needed">{fmtParams(size.value)} parameters</span>{:else if size.kind === 'bytes'}<span title="Size of the default weights">{bytes(size.value, 1)}</span>{/if}
      {#if hit?.license}<span title="License">{hit.license} license</span>{/if}
      {#if hit?.updatedAt}<span>updated {ago(hit.updatedAt, clock.now)}</span>{/if}
      {#if pageUrl}<a href={pageUrl} target="_blank" rel="noopener noreferrer" class="inline-flex items-center gap-1 text-accent hover:underline"><ExternalLink size={11} /> open on {siteName}</a>{/if}
    </div>
    <div class="mt-3 flex flex-wrap items-center gap-2">
      <Tabs size="sm" bind:value={tab} tabs={[{ id: 'files', label: 'Weights & fit' }, ...(caps?.card ? [{ id: 'card', label: 'Model card' }] : []), ...(revisions.length ? [{ id: 'revisions', label: revLabel.charAt(0).toUpperCase() + revLabel.slice(1) + 's', count: revisions.length }] : [])]} />
      {#if revisions.length}
        <label class="relative ml-auto inline-flex items-center gap-1.5 text-xs text-fg-muted" title="Switch {revLabel}">
          <GitBranch size={12} />
          <select class="input h-7 w-auto max-w-[14rem] py-0 pr-7 text-xs" value={currentRevision?.name ?? ''} onchange={(e) => { const r = revisions.find((x) => x.name === (e.currentTarget as HTMLSelectElement).value); if (r) pick(r); }} aria-label="Switch {revLabel}">
            {#each revisions as r (r.repo || r.name)}
              <option value={r.name}>{r.name}{r.sizeBytes ? ` · ${bytes(r.sizeBytes, 1)}` : ''}{r.default ? ' · default' : ''}</option>
            {/each}
          </select>
        </label>
      {/if}
    </div>
  {/snippet}

  <div class="px-5 py-4">
    {#if tab === 'files'}
      {#if inspecting}
        <div class="mb-4 text-sm text-fg-muted">Reading the file list and headers of <span class="font-mono text-fg">{curRepo}</span>, nothing is downloaded…</div>
        <Skeleton rows={6} />
      {:else if inspectError}
        <Empty compact title="Could not open {curRepo}" description={inspectError}>
          <Button size="sm" icon={RefreshCw} onclick={load}>Try again</Button>
        </Empty>
      {:else if inspect && model}
        <div class="flex flex-col gap-6">
          <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-fg-faint">
            <span>{revLabel} <span class="font-mono text-fg-muted">{model.revision || 'default'}</span></span>
            {#if model.commit}<span title="Identifies exactly this version of the files">commit <span class="font-mono text-fg-muted">{model.commit.slice(0, 12)}</span></span>{/if}
            <span>{model.artifacts.length} files</span>
            <span>checked {when(model.resolvedAt)}</span>
            <span class="ml-auto inline-flex items-center gap-1"><Copy text={model.repo} size={12} /> copy name</span>
          </div>

          <section>
            <div class="mb-1 flex flex-wrap items-center gap-3">
              <h3 class="text-[11px] font-semibold tracking-wider text-fg-faint uppercase">{descriptorTitle}</h3>
              <select class="input ml-auto h-7 w-auto py-0 pr-7 text-xs" bind:value={slotId} aria-label="Plan inside a slot" title="Check the fit against the whole machine or against one slot's share of it">
                <option value="">Fit against the whole machine</option>
                {#each [...live.slots.values()] as s (s.id)}<option value={s.id}>Fit inside {s.name}</option>{/each}
              </select>
            </div>
            <p class="mb-3 max-w-2xl text-xs leading-5 text-fg-muted">
              {#if inspect.descriptors.length > 1}
                The same model saved at different precisions. Fewer bits per weight means a smaller download and less memory at some cost in answer quality. The ones that fit come first, largest first, so the top row keeps the most quality this machine can hold.
              {:else if inspect.descriptors.length === 1}
                One set of weights. The fit column says whether it runs on this machine at the context lengths nebu plans for.
              {:else}
                Nothing here looks like weights nebu can read. It understands GGUF files and safetensors shards next to a config.json.
              {/if}
            </p>
            <div class="overflow-x-auto rounded-lg border border-line">
              <table class="tbl">
                <thead>
                  <tr>
                    {@render th('Weights', 'The name of one set of weight files, what you pull and later run. For GGUF it is the quantization, such as Q4_K_M.')}
                    {@render th('Precision', 'How compactly each weight is stored. Fewer bits means smaller and faster to load but a little less accurate.')}
                    {@render th('Parameters', 'How big the model is, in weights. 8B is eight billion. Bigger usually answers better and needs more memory.', true)}
                    {@render th('Download', 'What you would download, which is also roughly the memory the weights take.', true)}
                    {@render th('Fit', 'Whether the weights plus room for the conversation fit in this machine\'s memory. The matrix below has the detail per context length.')}
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {#each ordered as d (d.group)}
                    {@const stored = storedModel(d.group)}
                    {@const task = pulling(d.group)}
                    {@const p = precision(d)}
                    {@const fit = fitSummary(inspect.rows, d.group)}
                    {@const runners = runtimeNames(d.formatId)}
                    <tr>
                      <td>
                        <div class="flex items-center gap-2 font-mono text-xs text-fg">
                          {d.group}
                          {#if d.group === bestGroup && ordered.length > 1}<span class="rounded bg-accent/15 px-1.5 py-0.5 font-sans text-[10px] font-medium text-accent" title="The largest weights that fit this machine">largest that fits</span>{/if}
                        </div>
                        <div class="mt-0.5 flex flex-wrap items-center gap-1.5 text-[11px] text-fg-faint">
                          <span class="font-mono" title={formatBlurb[d.formatId] ?? d.formatId}>{d.formatId}</span>
                          {#if d.architecture}<span title="Architecture, the model family the runtime has to support">· {d.architecture}</span>{/if}
                        </div>
                      </td>
                      <td class="max-w-[22rem]">
                        <div class="flex items-center gap-2">
                          <span class="flex items-center gap-0.5" title="Quality kept, out of five">
                            {#each [1, 2, 3, 4, 5] as i (i)}
                              <span class="h-2 w-1.5 rounded-sm {i <= p.level ? (p.tone === 'ok' ? 'bg-ok' : p.tone === 'accent' ? 'bg-accent' : p.tone === 'warn' ? 'bg-warn' : p.tone === 'bad' ? 'bg-bad' : 'bg-fg-faint') : 'bg-line'}"></span>
                            {/each}
                          </span>
                          <span class="text-xs text-fg">{p.label}</span>
                        </div>
                        <div class="mt-0.5 text-[11px] leading-4 text-fg-faint">{p.blurb}</div>
                      </td>
                      <td class="num text-xs">{fmtParams(d.parameterCount)}</td>
                      <td class="num text-xs whitespace-nowrap">{bytes(d.totalBytes, 1)}</td>
                      <td>
                        {#if fit}
                          <Badge tone={fit.tone} size="xs" dot label={fit.label} />
                          {#if planRuntimes.length > 1}<div class="mt-0.5 text-[11px] text-fg-faint">on {fit.runtime}</div>{/if}
                        {:else if runners}
                          <span class="text-xs text-fg-faint" title="No plan came back, see the warnings below">Not planned</span>
                        {:else}
                          <span class="text-xs text-fg-faint" title="No known runtime serves {d.formatId} files">No runtime for {d.formatId}</span>
                        {/if}
                      </td>
                      <td class="text-right whitespace-nowrap">
                        {#if task}
                          <TaskChip {task} label="Pulling" />
                        {:else if stored}
                          <span class="inline-flex items-center gap-2">
                            <Badge tone="ok" size="xs" dot label="in store" />
                            <Button size="xs" variant="primary" icon={Play} onclick={() => runModel(stored)}>Run</Button>
                          </span>
                        {:else}
                          <Button size="xs" icon={Download} onclick={() => pull(d.group)} title="Download these weights into the store">Pull</Button>
                        {/if}
                      </td>
                    </tr>
                  {:else}
                    <tr><td colspan="6" class="text-sm text-fg-faint">No weights nebu can read. The files below are what {siteName} lists; a GGUF file or safetensors shards with a config.json would show up here.</td></tr>
                  {/each}
                </tbody>
              </table>
            </div>
          </section>

          {#if inspect.rows.length}
            <section>
              <h3 class="mb-1 text-[11px] font-semibold tracking-wider text-fg-faint uppercase">Fit by context length{slotName ? ` inside ${slotName}` : ''}</h3>
              <p class="mb-3 max-w-2xl text-xs leading-5 text-fg-muted">
                Context length is how much text the model holds at once, the prompt plus its reply, in tokens. Longer contexts need more memory for the model's working cache on top of the weights. Click a cell for the plan behind it.
              </p>
              <FitTable rows={inspect.rows} />
            </section>
          {/if}

          {#if inspect.warnings.length}
            <div class="rounded-lg border border-warn/30 bg-warn/8 px-3 py-2 text-xs leading-5 text-warn">
              {#each inspect.warnings as w (w)}<div>{w}</div>{/each}
            </div>
          {/if}

          {#if otherFiles.length || inspect.descriptors.length === 0}
            <section>
              <button class="flex items-center gap-1.5 text-[11px] font-semibold tracking-wider text-fg-faint uppercase hover:text-fg" onclick={() => (showFiles = !showFiles)}>
                <ChevronDown size={12} class="transition-transform {showFiles ? 'rotate-180' : ''}" />
                {inspect.descriptors.length === 0 ? 'Files' : 'Other files'} ({inspect.descriptors.length === 0 ? model.artifacts.length : otherFiles.length})
              </button>
              {#if showFiles}
                <div class="mt-2 overflow-x-auto rounded-lg border border-line">
                  <table class="tbl">
                    <thead><tr><th>path</th><th>role</th><th>format</th><th class="num">size</th></tr></thead>
                    <tbody>
                      {#each inspect.descriptors.length === 0 ? model.artifacts : otherFiles as a (a.path)}
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
              {/if}
            </section>
          {/if}
        </div>
      {/if}
    {:else if tab === 'card'}
      {#if card?.markdown || card?.html}
        <Markdown markdown={card.markdown} html={card.html} />
      {:else if cardError}
        <Empty compact title="Could not load the card" description={cardError} />
      {:else if card}
        <Empty compact title="No card published" description="This repository has no README or description.">
          {#if card.url}<Button size="sm" href={card.url} icon={ExternalLink}>Open the page</Button>{/if}
        </Empty>
      {:else}
        <Skeleton rows={8} />
      {/if}
    {:else if tab === 'revisions'}
      <p class="mb-3 text-xs leading-5 text-fg-muted">Every {revLabel} this repository publishes. Viewing one reads its files and plans its fit; the store keeps each {revLabel} you pull apart.</p>
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
                <td class="text-right">{#if current}<Badge size="xs" tone="accent" label="viewing" />{:else}<Button size="xs" variant="ghost" onclick={() => pick(r)}>View</Button>{/if}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </div>

  {#snippet footer()}
    <span class="text-xs text-fg-faint">Nothing is downloaded until you pull.</span>
    <div class="ml-auto flex gap-2">
      <Button size="sm" variant={watched ? 'ghost' : 'outline'} icon={watched ? Check : Eye} disabled={watched} onclick={() => (watchOpen = true)} title="Get a finding when this repository changes on {siteName}">{watched ? 'Watching' : 'Watch for changes'}</Button>
    </div>
  {/snippet}
</Drawer>

<WatchDialog bind:open={watchOpen} {sourceId} repo={model?.repo ?? curRepo} />
