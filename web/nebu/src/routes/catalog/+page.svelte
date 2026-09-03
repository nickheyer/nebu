<script lang="ts">
  import { page } from '$app/state';
  import { api, message } from '$lib/api';
  import { live, clock, taskFor, modelKey } from '$lib/state.svelte';
  import { runModel } from '$lib/slotActions.svelte';
  import { ago, bytes, count, params as fmtParams, splitRepo, when } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import type { SearchHit, Source } from '$proto/source_pb';
  import type { InspectResponse } from '$proto/estimate_pb';
  import { Search, Download, Eye, Play, Check, Compass, Heart, ArrowDownToLine } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Panel from '$lib/components/ui/Panel.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Badge from '$lib/components/ui/Badge.svelte';
  import Skeleton from '$lib/components/ui/Skeleton.svelte';
  import Copy from '$lib/components/ui/Copy.svelte';
  import FitTable from '$lib/components/FitTable.svelte';
  import TaskChip from '$lib/components/TaskChip.svelte';
  import WatchDialog from '$lib/components/WatchDialog.svelte';

  let sources = $state<Source[]>([]);
  let sourceId = $state('');
  let query = $state('');
  let hits = $state<SearchHit[]>([]);
  let searched = $state(false);
  let searching = $state(false);
  let selected = $state('');
  let inspecting = $state(false);
  let inspect = $state<InspectResponse | null>(null);
  let inspectError = $state('');
  let slotId = $state('');
  let watchOpen = $state(false);

  const looksLikeRepo = $derived(/^[\w.-]+\/[\w.-]+$/.test(query.trim()));
  const watched = $derived(!!selected && [...live.watches.values()].some((w) => w.sourceId === sourceId && w.repo === selected));

  $effect(() => {
    api.sources
      .listSources({})
      .then((r) => {
        sources = r.sources;
        if (!sourceId && sources.length) sourceId = sources[0].id;
        const repo = page.url.searchParams.get('repo');
        const src = page.url.searchParams.get('source');
        if (src && sources.some((s) => s.id === src)) sourceId = src;
        if (repo && !selected) open(repo);
      })
      .catch((err) => fail(err, 'Could not list sources'));
  });

  async function search() {
    if (!query.trim()) return;
    searching = true;
    searched = true;
    try {
      hits = (await api.sources.search({ sourceId, query: query.trim(), limit: 40 })).hits;
    } catch (err) {
      fail(err, 'Search failed');
    } finally {
      searching = false;
    }
  }

  async function open(repo: string) {
    selected = repo;
    inspect = null;
    inspectError = '';
    inspecting = true;
    try {
      inspect = await api.estimate.inspect({ sourceId, repo, slotId });
    } catch (err) {
      inspectError = message(err);
    } finally {
      inspecting = false;
    }
  }

  async function pull(group: string) {
    try {
      const r = await api.store.pull({ sourceId, repo: selected, group });
      ok(`Pulling ${selected}`, group, r.task ? { href: `/tasks?id=${r.task.id}`, label: 'Follow the pull' } : undefined);
    } catch (err) {
      fail(err, 'Pull refused');
    }
  }

  function storedModel(group: string) {
    return live.models.get(modelKey({ sourceId, repo: selected, group }));
  }
  function pulling(group: string) {
    return taskFor('pull', { source: sourceId, repo: selected, group });
  }
</script>

<PageHeader title="Catalog" description="Search a source, read headers without downloading, and see what fits before you pull" />

<form
  class="mb-5 flex flex-wrap items-center gap-2"
  onsubmit={(e) => {
    e.preventDefault();
    if (looksLikeRepo && !hits.some((h) => h.repo === query.trim())) open(query.trim());
    else search();
  }}
>
  <select class="input w-40 shrink-0" bind:value={sourceId} aria-label="Source">
    {#each sources as s (s.id)}<option value={s.id}>{s.id}</option>{/each}
  </select>
  <div class="relative min-w-64 flex-1">
    <Search size={15} class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-fg-faint" />
    <input class="input pl-9" bind:value={query} placeholder="Search models, or type org/repo to inspect it directly" autocomplete="off" />
  </div>
  <select class="input w-56 shrink-0" bind:value={slotId} aria-label="Plan inside a slot" title="Plan fits inside a slot's devices and budget">
    <option value="">Fit on the whole host</option>
    {#each [...live.slots.values()] as s (s.id)}<option value={s.id}>Fit inside {s.name}</option>{/each}
  </select>
  <Button type="submit" variant="primary" loading={searching} icon={looksLikeRepo ? Eye : Search}>{looksLikeRepo ? 'Inspect' : 'Search'}</Button>
</form>

<div class="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
  <Panel flush title="Results" description={searched ? `${hits.length} from ${sourceId}` : ''}>
    {#if hits.length === 0}
      <Empty compact icon={Compass} title={searched ? 'No matches' : 'Search a source'} description={searched ? 'Try a broader term, or type an exact org/repo above.' : 'Results show download and like counts so you can pick the maintained repo.'} />
    {:else}
      <ul class="max-h-[70vh] divide-y divide-line/60 overflow-y-auto">
        {#each hits as h (h.repo)}
          {@const r = splitRepo(h.repo)}
          <li>
            <button class="flex w-full flex-col gap-1 px-4 py-2.5 text-left transition-colors hover:bg-raised/50 {selected === h.repo ? 'bg-raised/70' : ''}" onclick={() => open(h.repo)}>
              <div class="flex items-center gap-2">
                <span class="truncate font-mono text-sm"><span class="text-fg-muted">{r.org}/</span><span class="text-fg">{r.name}</span></span>
                {#if [...live.models.values()].some((m) => m.repo === h.repo && m.sourceId === sourceId)}<Badge tone="ok" size="xs" label="stored" />{/if}
              </div>
              <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11.5px] text-fg-faint">
                <span class="inline-flex items-center gap-1 tabular-nums"><ArrowDownToLine size={11} />{count(h.downloads)}</span>
                <span class="inline-flex items-center gap-1 tabular-nums"><Heart size={11} />{count(h.likes)}</span>
                <span>{ago(h.updatedAt, clock.now)}</span>
                {#each h.tags.filter((t) => !t.includes(':')).slice(0, 4) as t (t)}
                  <span class="rounded bg-raised px-1 text-fg-muted">{t}</span>
                {/each}
              </div>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </Panel>

  <Panel flush>
    {#if inspecting}
      <div class="p-5">
        <div class="mb-4 text-sm text-fg-muted">Resolving <span class="font-mono text-fg">{selected}</span> and reading headers…</div>
        <Skeleton rows={6} />
      </div>
    {:else if inspectError}
      <Empty compact title="Could not inspect {selected}" description={inspectError}>
        <Button size="sm" onclick={() => open(selected)}>Try again</Button>
      </Empty>
    {:else if inspect}
      <div class="flex flex-col gap-5 p-5">
        <div class="flex flex-wrap items-start gap-3">
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <h2 class="truncate font-mono text-base font-semibold text-fg">{inspect.model?.repo}</h2>
              <Copy text={inspect.model?.repo ?? ''} size={13} />
            </div>
            <div class="mt-1 flex flex-wrap gap-x-3 text-xs text-fg-faint">
              <span>revision <span class="font-mono text-fg-muted">{inspect.model?.revision || 'default'}</span></span>
              {#if inspect.model?.commit}<span>commit <span class="font-mono text-fg-muted">{inspect.model.commit.slice(0, 12)}</span></span>{/if}
              <span>{inspect.model?.artifacts.length} files</span>
              <span>resolved {when(inspect.model?.resolvedAt)}</span>
            </div>
          </div>
          <Button size="sm" variant={watched ? 'ghost' : 'outline'} icon={watched ? Check : Eye} disabled={watched} onclick={() => (watchOpen = true)}>{watched ? 'Watching' : 'Watch'}</Button>
        </div>

        <section>
          <h3 class="mb-2 text-[11px] font-semibold tracking-wider text-fg-faint uppercase">Weight groups</h3>
          <div class="overflow-x-auto rounded-lg border border-line">
            <table class="tbl">
              <thead><tr><th>group</th><th>arch</th><th class="num">params</th><th class="num">bpw</th><th class="num">size</th><th></th></tr></thead>
              <tbody>
                {#each inspect.descriptors as d (d.group)}
                  {@const stored = storedModel(d.group)}
                  {@const task = pulling(d.group)}
                  <tr>
                    <td class="font-mono text-xs text-fg">{d.group}</td>
                    <td class="text-xs">{d.architecture || '–'}</td>
                    <td class="num text-xs">{fmtParams(d.parameterCount)}</td>
                    <td class="num text-xs">{d.bitsPerWeight ? d.bitsPerWeight.toFixed(2) : '–'}</td>
                    <td class="num text-xs">{bytes(d.totalBytes)}</td>
                    <td class="text-right whitespace-nowrap">
                      {#if task}
                        <TaskChip {task} label="Pulling" />
                      {:else if stored}
                        <span class="inline-flex items-center gap-2">
                          <Badge tone="ok" size="xs" dot label="stored" />
                          <Button size="xs" variant="primary" icon={Play} onclick={() => runModel(stored)}>Run</Button>
                        </span>
                      {:else}
                        <Button size="xs" icon={Download} onclick={() => pull(d.group)}>Pull</Button>
                      {/if}
                    </td>
                  </tr>
                {:else}
                  <tr><td colspan="6" class="text-sm text-fg-faint">No weight groups were recognised in this repository.</td></tr>
                {/each}
              </tbody>
            </table>
          </div>
        </section>

        {#if inspect.rows.length}
          <section>
            <h3 class="mb-2 text-[11px] font-semibold tracking-wider text-fg-faint uppercase">Fit by context length{slotId ? ` inside ${live.slots.get(slotId)?.name ?? 'slot'}` : ''}</h3>
            <FitTable rows={inspect.rows} />
          </section>
        {/if}

        {#if inspect.warnings.length}
          <div class="rounded-lg border border-warn/30 bg-warn/8 px-3 py-2 text-xs leading-5 text-warn">
            {#each inspect.warnings as w (w)}<div>{w}</div>{/each}
          </div>
        {/if}
      </div>
    {:else}
      <Empty icon={Eye} title="Pick a repository" description="Weight groups, architecture, and a fit table per runtime and context length appear here. Nothing is downloaded until you pull." />
    {/if}
  </Panel>
</div>

<WatchDialog bind:open={watchOpen} {sourceId} repo={selected} />
