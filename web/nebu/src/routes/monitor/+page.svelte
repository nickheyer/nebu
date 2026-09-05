<script lang="ts">
  import { api } from '$lib/api';
  import { live, cached, clock, slotName, taskFor, sourceName, profileName } from '$lib/state.svelte';
  import { groupByProvider } from '$lib/catalog';
  import { ago, byName, newestFirst, plural, when } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { FindingKind, type Watch, type Want } from '$proto/monitor_pb';
  import { SourceKind } from '$proto/source_pb';
  import type { Tone } from '$lib/format';
  import { RefreshCw, Trash2, Check, CheckCheck, ExternalLink, ListFilter, X, Radar, Eye, Search } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Segmented from '$lib/components/ui/Segmented.svelte';
  import WatchDialog from '$lib/components/WatchDialog.svelte';
  import WantDialog from '$lib/components/WantDialog.svelte';
  import TaskChip from '$lib/components/TaskChip.svelte';
  import SkeletonRows from '$lib/components/ui/SkeletonRows.svelte';

  let addOpen = $state(false);
  let wantOpen = $state(false);
  let view = $state('open');
  let checkingAll = $state(false);
  // Narrows the findings to one watch or want, everything when empty
  let only = $state<Watch | Want | null>(null);

  const loading = $derived(!live.ready && !live.error);
  const providers = $derived(groupByProvider(cached.sources));
  const watches = $derived([...live.watches.values()].sort(byName((w) => w.repo)));
  const wants = $derived([...live.wants.values()].sort((a, b) => Number(a.satisfied) - Number(b.satisfied) || a.query.localeCompare(b.query)));
  const findings = $derived(
    [...live.findings.values()].filter((f) => !only || ('query' in only ? f.wantId === only.id : f.watchId === only.id)).sort(newestFirst((f) => f.foundAt))
  );
  const open = $derived(findings.filter((f) => !f.acknowledged));
  const shown = $derived(view === 'open' ? open : findings);

  $effect(() => {
    // The snapshot only carries unacknowledged findings, so load the rest once
    api.monitor
      .listFindings({})
      .then((r) => {
        for (const f of r.findings) live.findings.set(f.id, f);
      })
      .catch((err) => fail(err, 'Could not list findings'));
  });

  // A finding belongs to the want that searched or the watch that looked
  function ownerOf(f: { wantId: string; watchId: string }): string {
    return f.wantId ? (live.wants.get(f.wantId)?.query ?? '') : (live.watches.get(f.watchId)?.repo ?? '');
  }

  function labelOf(w: Watch | Want): string {
    return 'query' in w ? w.query : w.repo;
  }

  async function check(w?: Watch | Want, rearm = false) {
    if (rearm && w && 'query' in w) {
      const yes = await confirm({ title: `Look again for ${w.query}?`, message: 'What it found is forgotten.', action: 'Look again' });
      if (!yes) return;
    }
    if (!w) checkingAll = true;
    try {
      const r = await api.monitor.checkWatches({ id: w?.id ?? '', rearm });
      ok(w ? `Checking ${labelOf(w)}` : 'Checking everything', undefined, r.task ? { href: `/tasks?id=${r.task.id}`, label: 'Task' } : undefined);
    } catch (err) {
      fail(err, 'Check refused');
    } finally {
      checkingAll = false;
    }
  }

  async function remove(w: Watch) {
    const yes = await confirm({ title: `Stop watching ${w.repo}?`, message: 'Its findings go too. Pulled models stay.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.monitor.removeWatch({ id: w.id });
      for (const f of [...live.findings.values()]) if (f.watchId === w.id) live.findings.delete(f.id);
      if (only?.id === w.id) only = null;
      ok(`Stopped watching ${w.repo}`);
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }

  async function unwant(w: Want) {
    const yes = await confirm({ title: `Stop wanting ${w.query}?`, message: 'Its findings go too. Pulled models stay.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.monitor.removeWant({ id: w.id });
      for (const f of [...live.findings.values()]) if (f.wantId === w.id) live.findings.delete(f.id);
      if (only?.id === w.id) only = null;
      ok(`Stopped wanting ${w.query}`);
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }

  // Where a want looks: one source by name, one provider by name, or everywhere
  function whereOf(w: Want): string {
    if (w.sourceId) return sourceName(w.sourceId);
    if (w.kind !== SourceKind.UNSPECIFIED) return providers.find((g) => g.kind === w.kind)?.name ?? (SourceKind[w.kind] ?? '').toLowerCase();
    return 'all sources';
  }

  async function ack(id: string) {
    try {
      await api.monitor.ackFinding({ id });
    } catch (err) {
      fail(err, 'Acknowledge failed');
    }
  }

  async function ackAll() {
    for (const f of open) await ack(f.id);
  }

  const kindTone: Record<number, Tone> = { [FindingKind.NEW_REVISION]: 'info', [FindingKind.NEW_GROUP]: 'ok', [FindingKind.REMOVED_GROUP]: 'warn', [FindingKind.WANTED_FOUND]: 'accent' };
  const kindLabel: Record<number, string> = { [FindingKind.NEW_REVISION]: 'New revision', [FindingKind.NEW_GROUP]: 'New weights', [FindingKind.REMOVED_GROUP]: 'Removed weights', [FindingKind.WANTED_FOUND]: 'Found' };
</script>

{#snippet then(w: Watch | Want)}
  {#if w.autoPull && w.slotId}
    <div class="text-fg">Pull and swap into <span class="font-mono">{slotName(w.slotId)}</span></div>
    <div class="text-xs text-fg-faint">{[w.runtimeId || 'slot runtime', profileName(w.profileId) || 'default profile'].join(' · ')}</div>
  {:else if w.autoPull}
    <span class="text-fg">Pull</span>
  {:else}
    <span class="text-fg-muted">Record</span>
  {/if}
{/snippet}

{#snippet taskLinks(f: { taskId: string; swapTaskId: string })}
  {#if f.taskId}<a href="/tasks?id={f.taskId}" class="link inline-flex items-center gap-1 text-xs"><ExternalLink size={11} />pull</a>{/if}
  {#if f.swapTaskId}<a href="/tasks?id={f.swapTaskId}" class="link inline-flex items-center gap-1 text-xs"><ExternalLink size={11} />swap</a>{/if}
{/snippet}

{#snippet wantHead()}
  <thead><tr><th>Query</th><th>Where</th><th>Match</th><th>On match</th><th>Found</th><th>Checked</th><th></th></tr></thead>
{/snippet}

{#snippet watchHead()}
  <thead><tr><th>Repository</th><th>Source</th><th>Commit</th><th class="num">Groups</th><th>Match</th><th>On change</th><th>Checked</th><th></th></tr></thead>
{/snippet}

{#snippet findingHead()}
  <thead><tr><th>Kind</th><th>Repository</th><th>Detail</th><th>From</th><th>Found</th><th></th></tr></thead>
{/snippet}

<PageHeader title="Monitor">
  <Button icon={RefreshCw} loading={checkingAll} disabled={!watches.length && !wants.length} onclick={() => check()}>Check now</Button>
  <Button icon={Search} onclick={() => (wantOpen = true)}>Want a model</Button>
  <Button variant="primary" icon={Eye} onclick={() => (addOpen = true)}>Watch a repository</Button>
</PageHeader>

<div class="flex flex-col gap-9">
  <Section title="Findings" count={open.length || undefined} meta={only ? `from ${labelOf(only)}` : ''}>
    {#snippet actions()}
      {#if only}<Button size="sm" variant="ghost" icon={X} onclick={() => (only = null)}>All</Button>{/if}
      {#if findings.length}<Segmented size="sm" bind:value={view} tabs={[{ id: 'open', label: 'New', count: open.length }, { id: 'all', label: 'All', count: findings.length }]} />{/if}
      {#if open.length}<Button size="sm" variant="ghost" icon={CheckCheck} onclick={ackAll}>Acknowledge all</Button>{/if}
    {/snippet}
    {#if loading}
      <table class="tbl">
        {@render findingHead()}
        <tbody><SkeletonRows rows={3} cols={[{ w: 'w-20' }, { w: 'w-48', sub: true }, 'w-64', 'w-32', 'w-14', { w: 'w-10', num: true }]} /></tbody>
      </table>
    {:else if shown.length === 0}
      <Empty compact icon={Radar} title={view === 'open' ? 'Nothing new' : 'No findings'} />
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          {@render findingHead()}
          <tbody>
            {#each shown as f (f.id)}
              <tr class={f.acknowledged ? 'opacity-60' : ''}>
                <td><State tone={kindTone[f.kind] ?? 'neutral'} label={kindLabel[f.kind] ?? 'Finding'} /></td>
                <td>
                  <a href="/catalog?source={f.sourceId || live.watches.get(f.watchId)?.sourceId || ''}&repo={encodeURIComponent(f.repo)}" class="text-fg hover:text-accent">{f.repo}</a>
                  <div class="font-mono text-xs text-fg-faint">{[f.group, f.commit ? `@ ${f.commit.slice(0, 10)}` : ''].filter(Boolean).join(' ')}</div>
                </td>
                <td class="max-w-md truncate text-fg-muted" title={f.detail}>{f.detail}</td>
                <td>
                  <div class="truncate {f.wantId ? 'text-accent' : 'text-fg'}" title={ownerOf(f)}>{ownerOf(f) || '–'}</div>
                  {#if f.sourceId}<div class="text-xs text-fg-faint">{sourceName(f.sourceId)}</div>{/if}
                </td>
                <td class="whitespace-nowrap text-fg-muted" title={when(f.foundAt)}>
                  {ago(f.foundAt, clock.now)}
                  <div class="flex items-center gap-2">{@render taskLinks(f)}</div>
                </td>
                <td class="actions">
                  <span>{#if !f.acknowledged}<IconButton size="sm" icon={Check} label="Acknowledge" onclick={() => ack(f.id)} />{/if}</span>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Section>

  <Section title="Wants" count={wants.length || undefined} meta={wants.length ? `${wants.filter((w) => !w.satisfied).length} looking` : ''} info="A search run on every check until a weight group matches">
    {#if loading}
      <table class="tbl">
        {@render wantHead()}
        <tbody><SkeletonRows rows={2} cols={[{ w: 'w-40' }, 'w-20', 'w-16', 'w-24', 'w-10', 'w-14', { w: 'w-6', num: true }]} /></tbody>
      </table>
    {:else if wants.length === 0}
      <Empty compact title="Nothing wanted">
        <Button size="sm" icon={Search} onclick={() => (wantOpen = true)}>Want a model</Button>
      </Empty>
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          {@render wantHead()}
          <tbody>
            {#each wants as w (w.id)}
              {@const task = taskFor('check', { watch: w.id })}
              <tr class={w.satisfied ? 'opacity-70' : ''}>
                <td>
                  <span class="text-fg">{w.query}</span>
                  {#if task}<div class="mt-1"><TaskChip {task} label="Checking" /></div>{/if}
                </td>
                <td class="text-fg-muted">{whereOf(w)}</td>
                <td class="font-mono text-xs text-fg-muted">{[w.formatId, w.groupMatch].filter(Boolean).join(' ') || 'any'}</td>
                <td>{@render then(w)}</td>
                <td>
                  {#if w.satisfied}
                    <a href="/catalog?source={w.foundSourceId}&repo={encodeURIComponent(w.foundRepo)}" class="font-mono text-xs text-fg hover:underline">{w.foundRepo}</a>
                    <span class="font-mono text-xs text-fg-muted">{w.foundGroup}</span>
                    <span class="ml-1 inline-flex items-center gap-2">{@render taskLinks(w)}</span>
                  {:else}
                    <span class="text-fg-faint">Not yet</span>
                  {/if}
                </td>
                <td class="text-fg-muted" title={when(w.checkedAt)}>
                  {ago(w.checkedAt, clock.now)}
                  {#if w.error}<div class="max-w-xs truncate text-xs text-bad" title={w.error}>{w.error}</div>{/if}
                </td>
                <td class="actions">
                  <span>
                    <Menu
                      size="sm"
                      items={[
                        w.satisfied ? { label: 'Look again', icon: RefreshCw, onSelect: () => check(w, true) } : { label: 'Check now', icon: RefreshCw, onSelect: () => check(w) },
                        { label: 'Its findings', icon: ListFilter, onSelect: () => (only = w) },
                        { label: '', separator: true },
                        { label: 'Remove', icon: Trash2, tone: 'bad', onSelect: () => unwant(w) }
                      ]}
                    />
                  </span>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Section>

  <Section title="Watches" count={watches.length || undefined} info="One repository checked on an interval for new commits and weight groups">
    {#if loading}
      <table class="tbl">
        {@render watchHead()}
        <tbody><SkeletonRows rows={2} cols={[{ w: 'w-48' }, 'w-20', 'w-20', { w: 'w-6', num: true }, 'w-16', 'w-24', 'w-14', { w: 'w-6', num: true }]} /></tbody>
      </table>
    {:else if watches.length === 0}
      <Empty compact title="Nothing watched">
        <Button size="sm" icon={Eye} onclick={() => (addOpen = true)}>Watch a repository</Button>
      </Empty>
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          {@render watchHead()}
          <tbody>
            {#each watches as w (w.id)}
              {@const task = taskFor('check', { watch: w.id })}
              <tr>
                <td>
                  <a href="/catalog?source={w.sourceId}&repo={encodeURIComponent(w.repo)}" class="text-fg hover:text-accent">{w.repo}</a>
                  {#if w.revision}<span class="font-mono text-xs text-fg-faint"> @{w.revision}</span>{/if}
                  {#if task}<div class="mt-1"><TaskChip {task} label="Checking" /></div>{/if}
                </td>
                <td class="text-fg-muted">{sourceName(w.sourceId)}</td>
                <td class="font-mono text-xs text-fg-muted">{w.lastCommit ? w.lastCommit.slice(0, 10) : '–'}</td>
                <td class="num" title={w.knownGroups.join('\n')}>{w.knownGroups.length}</td>
                <td class="font-mono text-xs text-fg-muted">{w.groupMatch || 'any'}</td>
                <td>{@render then(w)}</td>
                <td class="text-fg-muted" title={when(w.checkedAt)}>
                  {ago(w.checkedAt, clock.now)}
                  {#if w.error}<div class="max-w-xs truncate text-xs text-bad" title={w.error}>{w.error}</div>{/if}
                </td>
                <td class="actions">
                  <span>
                    <Menu
                      size="sm"
                      items={[
                        { label: 'Check now', icon: RefreshCw, onSelect: () => check(w) },
                        { label: 'Its findings', icon: ListFilter, onSelect: () => (only = w) },
                        { label: 'Open in the catalog', icon: ExternalLink, href: `/catalog?source=${w.sourceId}&repo=${encodeURIComponent(w.repo)}` },
                        { label: '', separator: true },
                        { label: 'Remove', icon: Trash2, tone: 'bad', onSelect: () => remove(w) }
                      ]}
                    />
                  </span>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Section>
</div>

<WatchDialog bind:open={addOpen} />
<WantDialog bind:open={wantOpen} />
