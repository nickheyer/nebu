<script lang="ts">
  import { api } from '$lib/api';
  import { live, clock, slotName, taskFor } from '$lib/state.svelte';
  import { ago, enumLabel, when } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { FindingKind, type Watch, type Want } from '$proto/monitor_pb';
  import { SourceKind } from '$proto/source_pb';
  import { Radar, Plus, RefreshCw, Trash2, Check, CheckCheck, Eye, ExternalLink, Sparkles, ListFilter, X } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Panel from '$lib/components/ui/Panel.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Badge from '$lib/components/ui/Badge.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import Tabs from '$lib/components/ui/Tabs.svelte';
  import WatchDialog from '$lib/components/WatchDialog.svelte';
  import WantDialog from '$lib/components/WantDialog.svelte';
  import TaskChip from '$lib/components/TaskChip.svelte';

  let addOpen = $state(false);
  let wantOpen = $state(false);
  let view = $state('open');
  let checkingAll = $state(false);
  // Narrows the findings to one watch or want, everything when empty
  let only = $state<Watch | Want | null>(null);

  const watches = $derived([...live.watches.values()].sort((a, b) => a.repo.localeCompare(b.repo)));
  const wants = $derived([...live.wants.values()].sort((a, b) => Number(a.satisfied) - Number(b.satisfied) || a.query.localeCompare(b.query)));
  const findings = $derived([...live.findings.values()].filter((f) => !only || ('query' in only ? f.wantId === only.id : f.watchId === only.id)).sort((a, b) => Number((b.foundAt?.seconds ?? 0n) - (a.foundAt?.seconds ?? 0n))));
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

  // A finding belongs to the want that searched or the watch that looked, and names where it came from
  function ownerOf(f: { wantId: string; watchId: string }): string {
    return f.wantId ? (live.wants.get(f.wantId)?.query ?? '') : (live.watches.get(f.watchId)?.repo ?? '');
  }

  function labelOf(w: Watch | Want): string {
    return 'query' in w ? w.query : w.repo;
  }

  async function check(w?: Watch | Want, rearm = false) {
    if (rearm && w && 'query' in w) {
      const yes = await confirm({ title: `Look again for ${w.query}?`, message: `What it found is forgotten and the next match is ${w.autoPull ? 'pulled and swapped' : 'recorded'} the same way.`, action: 'Look again' });
      if (!yes) return;
    }
    if (!w) checkingAll = true;
    try {
      const r = await api.monitor.checkWatches({ id: w?.id ?? '', rearm });
      const what = w ? ('repo' in w ? `Checking ${w.repo}` : `Looking for ${w.query}`) : 'Checking every watch and want';
      ok(what, undefined, r.task ? { href: `/tasks?id=${r.task.id}`, label: 'Follow' } : undefined);
    } catch (err) {
      fail(err, 'Check refused');
    } finally {
      checkingAll = false;
    }
  }

  async function remove(w: Watch) {
    const yes = await confirm({ title: `Stop watching ${w.repo}?`, message: 'Its findings are removed too. Pulled models stay in the store.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.monitor.removeWatch({ id: w.id });
      for (const f of [...live.findings.values()]) if (f.watchId === w.id) live.findings.delete(f.id);
      ok(`Stopped watching ${w.repo}`);
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }

  async function unwant(w: Want) {
    const yes = await confirm({ title: `Stop wanting ${w.query}?`, message: 'Its findings are removed too. Anything pulled stays in the store.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.monitor.removeWant({ id: w.id });
      for (const f of [...live.findings.values()]) if (f.wantId === w.id) live.findings.delete(f.id);
      ok(`No longer wanting ${w.query}`);
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }

  function whereOf(w: Want): string {
    if (w.sourceId) return live.sources.get(w.sourceId)?.name || w.sourceId;
    if (w.kind !== SourceKind.UNSPECIFIED) return (SourceKind[w.kind] ?? '').toLowerCase();
    return 'every source';
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

  const kindTone: Record<number, 'ok' | 'info' | 'warn' | 'neutral' | 'accent'> = { [FindingKind.NEW_REVISION]: 'info', [FindingKind.NEW_GROUP]: 'ok', [FindingKind.REMOVED_GROUP]: 'warn', [FindingKind.WANTED_FOUND]: 'accent' };
</script>

<PageHeader title="Monitor" description="Watched repositories checked on an interval, and wanted models searched for until they turn up">
  <Button variant="outline" icon={RefreshCw} loading={checkingAll} disabled={!watches.length && !wants.length} onclick={() => check()}>Check all now</Button>
  <Button variant="outline" icon={Sparkles} onclick={() => (wantOpen = true)}>Want a model</Button>
  <Button variant="primary" icon={Plus} onclick={() => (addOpen = true)}>Watch a repository</Button>
</PageHeader>

<div class="flex flex-col gap-6">
  <Panel title="Wanted" description="{wants.filter((w) => !w.satisfied).length} still looking" flush>
    {#if wants.length === 0}
      <Empty compact icon={Sparkles} title="Nothing wanted" description="Want a model that is not out yet, or not in a format you can run. Every source is searched each interval and the first matching weight group is pulled the moment it appears.">
        <Button size="sm" variant="primary" icon={Sparkles} onclick={() => (wantOpen = true)}>Want a model</Button>
      </Empty>
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          <thead><tr><th>search</th><th>where</th><th>match</th><th>on found</th><th>found</th><th>last look</th><th></th></tr></thead>
          <tbody>
            {#each wants as w (w.id)}
              {@const task = taskFor('check', { watch: w.id })}
              <tr class={w.satisfied ? 'opacity-70' : ''}>
                <td>
                  <span class="text-sm text-fg">{w.query}</span>
                  {#if task}<div class="mt-1"><TaskChip {task} label="Looking" /></div>{/if}
                </td>
                <td class="text-xs">{whereOf(w)}</td>
                <td class="font-mono text-xs text-fg-muted">{[w.formatId, w.groupMatch].filter(Boolean).join(' ') || 'any'}</td>
                <td class="text-xs">
                  {#if w.autoPull && w.slotId}
                    <Badge size="xs" tone="accent" label="pull and swap" /> <span class="text-fg-muted">{slotName(w.slotId)}</span>
                  {:else if w.autoPull}
                    <Badge size="xs" tone="info" label="pull" />
                  {:else}
                    <span class="text-fg-faint">record only</span>
                  {/if}
                </td>
                <td class="text-xs">
                  {#if w.satisfied}
                    <a href="/catalog?source={w.foundSourceId}&repo={encodeURIComponent(w.foundRepo)}" class="font-mono text-fg hover:underline">{w.foundRepo}</a>
                    <span class="font-mono text-fg-muted">{w.foundGroup}</span>
                    {#if w.taskId}<a href="/tasks?id={w.taskId}" class="ml-1 inline-flex items-center gap-1 text-[11px] text-accent hover:underline"><ExternalLink size={11} /> pull</a>{/if}
                    {#if w.swapTaskId}<a href="/tasks?id={w.swapTaskId}" class="ml-1 inline-flex items-center gap-1 text-[11px] text-accent hover:underline"><ExternalLink size={11} /> swap</a>{/if}
                  {:else}
                    <span class="text-fg-faint">not yet</span>
                  {/if}
                </td>
                <td class="text-xs text-fg-muted" title={when(w.checkedAt)}>
                  {ago(w.checkedAt, clock.now)}
                  {#if w.error}<div class="max-w-xs truncate text-bad" title={w.error}>{w.error}</div>{/if}
                </td>
                <td class="text-right">
                  <Menu
                    items={[
                      w.satisfied ? { label: 'Look again', icon: RefreshCw, onSelect: () => check(w, true) } : { label: 'Look now', icon: RefreshCw, onSelect: () => check(w) },
                      { label: 'Findings', icon: ListFilter, onSelect: () => (only = w) },
                      { label: '', separator: true },
                      { label: 'Stop wanting', icon: Trash2, tone: 'bad', onSelect: () => unwant(w) }
                    ]}
                  />
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Panel>

  <Panel title="Watches" description="{watches.length} watched" flush>
    {#if watches.length === 0}
      <Empty compact icon={Eye} title="Nothing watched" description="Watch a repository to be told about new commits and quants. Add auto pull and a slot to move to the freshest weights automatically.">
        <Button size="sm" variant="primary" icon={Plus} onclick={() => (addOpen = true)}>Watch a repository</Button>
      </Empty>
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          <thead><tr><th>repository</th><th>source</th><th>commit</th><th class="num">groups</th><th>match</th><th>on change</th><th>last check</th><th></th></tr></thead>
          <tbody>
            {#each watches as w (w.id)}
              {@const task = taskFor('check', { watch: w.id })}
              <tr>
                <td>
                  <a href="/catalog?source={w.sourceId}&repo={encodeURIComponent(w.repo)}" class="font-mono text-sm text-fg hover:underline">{w.repo}</a>
                  {#if w.revision}<span class="font-mono text-xs text-fg-faint">@{w.revision}</span>{/if}
                  {#if task}<div class="mt-1"><TaskChip {task} label="Checking" /></div>{/if}
                </td>
                <td class="text-xs">{w.sourceId}</td>
                <td class="font-mono text-xs text-fg-muted">{w.lastCommit ? w.lastCommit.slice(0, 10) : '–'}</td>
                <td class="num text-xs" title={w.knownGroups.join('\n')}>{w.knownGroups.length}</td>
                <td class="font-mono text-xs text-fg-muted">{w.groupMatch || 'any'}</td>
                <td class="text-xs">
                  {#if w.autoPull && w.slotId}
                    <Badge size="xs" tone="accent" label="pull and swap" /> <span class="text-fg-muted">{slotName(w.slotId)}</span>
                  {:else if w.autoPull}
                    <Badge size="xs" tone="info" label="pull" />
                  {:else}
                    <span class="text-fg-faint">record only</span>
                  {/if}
                </td>
                <td class="text-xs text-fg-muted" title={when(w.checkedAt)}>
                  {ago(w.checkedAt, clock.now)}
                  {#if w.error}<div class="max-w-xs truncate text-bad" title={w.error}>{w.error}</div>{/if}
                </td>
                <td class="text-right">
                  <Menu
                    items={[
                      { label: 'Check now', icon: RefreshCw, onSelect: () => check(w) },
                      { label: 'Findings', icon: ListFilter, onSelect: () => (only = w) },
                      { label: 'Inspect in catalog', icon: ExternalLink, href: `/catalog?source=${w.sourceId}&repo=${encodeURIComponent(w.repo)}` },
                      { label: '', separator: true },
                      { label: 'Stop watching', icon: Trash2, tone: 'bad', onSelect: () => remove(w) }
                    ]}
                  />
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Panel>

  <Panel title="Findings" description={only ? `of ${labelOf(only)}` : 'one row per change a check noticed'} flush>
    {#snippet actions()}
      {#if only}
        <button type="button" class="inline-flex h-7 items-center gap-1 rounded-md border border-accent/25 bg-accent/12 px-2 text-xs text-accent hover:bg-accent/20" onclick={() => (only = null)}><ListFilter size={12} /> {labelOf(only)} <X size={12} /></button>
      {/if}
      <Tabs size="sm" bind:value={view} tabs={[{ id: 'open', label: 'Open', count: open.length }, { id: 'all', label: 'All', count: findings.length }]} />
      {#if open.length}<Button size="sm" variant="ghost" icon={CheckCheck} onclick={ackAll}>Acknowledge all</Button>{/if}
    {/snippet}
    {#if shown.length === 0}
      <Empty compact icon={Radar} title={view === 'open' ? 'Nothing new' : 'No findings'} description="A finding records a changed commit, or a weight group that appeared or vanished, with the task it triggered." />
    {:else}
      <ul class="divide-y divide-line/60">
        {#each shown as f (f.id)}
          <li class="flex items-start gap-3 px-4 py-3 {f.acknowledged ? 'opacity-60' : ''}">
            <Badge size="xs" tone={kindTone[f.kind] ?? 'neutral'} label={enumLabel(FindingKind, f.kind)} class="mt-0.5 shrink-0" />
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-center gap-x-2 font-mono text-sm">
                <a href="/catalog?source={f.sourceId || live.watches.get(f.watchId)?.sourceId || ''}&repo={encodeURIComponent(f.repo)}" class="text-fg hover:underline">{f.repo}</a>
                {#if f.group}<span class="text-fg-muted">{f.group}</span>{/if}
                {#if f.commit}<span class="text-[11px] text-fg-faint">@ {f.commit.slice(0, 10)}</span>{/if}
              </div>
              <div class="mt-0.5 text-xs text-fg-muted">{f.detail}</div>
              <div class="mt-1 flex flex-wrap items-center gap-3 text-[11px] text-fg-faint">
                <span title={when(f.foundAt)}>{ago(f.foundAt, clock.now)}</span>
                {#if ownerOf(f)}<Badge size="xs" tone={f.wantId ? 'accent' : 'neutral'} label={ownerOf(f)} />{/if}
                {#if f.sourceId}<span>from <span class="text-fg-muted">{live.sources.get(f.sourceId)?.name || f.sourceId}</span></span>{/if}
                {#if f.taskId}<a href="/tasks?id={f.taskId}" class="inline-flex items-center gap-1 text-accent hover:underline"><ExternalLink size={11} /> pull</a>{/if}
                {#if f.swapTaskId}<a href="/tasks?id={f.swapTaskId}" class="inline-flex items-center gap-1 text-accent hover:underline"><ExternalLink size={11} /> swap</a>{/if}
              </div>
            </div>
            {#if !f.acknowledged}
              <Button size="xs" variant="ghost" icon={Check} onclick={() => ack(f.id)}>Ack</Button>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </Panel>
</div>

<WatchDialog bind:open={addOpen} />
<WantDialog bind:open={wantOpen} />
