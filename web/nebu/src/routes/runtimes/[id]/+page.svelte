<script lang="ts">
  import type { Component } from 'svelte';
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { tabState } from '$lib/tabs.svelte';
  import { api } from '$lib/api';
  import { live, cached, clock, taskFor, instanceLive, installsOf, startedTask } from '$lib/state.svelte';
  import { ago, enumLabel, newestFirst, when } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { ApiFlavor, InstallKind, ParamType, type InstallMethod } from '$proto/runtime_pb';
  import { BuildState, SandboxKind } from '$proto/recipe_pb';
  import { Check, Download, FolderInput, Hammer, ScrollText, Trash2 } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Tabs from '$lib/components/ui/Tabs.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Choices from '$lib/components/ui/Choices.svelte';
  import ParamList from '$lib/components/ui/ParamList.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import ConfigForm from '$lib/components/ConfigForm.svelte';
  import TaskChip from '$lib/components/TaskChip.svelte';

  const tabs = [
    { id: 'install', label: 'Install' },
    { id: 'installs', label: 'Installs' },
    { id: 'builds', label: 'Builds' },
    { id: 'params', label: 'Parameters' },
    { id: 'manifest', label: 'Manifest' }
  ];

  const id = $derived(page.params.id ?? '');
  const status = $derived(cached.runtimes.find((r) => r.manifest?.id === id));
  const manifest = $derived(status?.manifest);
  const installs = $derived(installsOf(id));
  const builds = $derived([...live.builds.values()].filter((b) => b.runtimeId === id).sort(newestFirst((b) => b.createdAt)));
  const task = $derived(taskFor('install', { runtime: id }) ?? taskFor('build', { runtime: id }));
  const options = $derived(status?.installs ?? []);
  const tab = tabState(() => tabs.map((t) => t.id), () => 'install');
  // An installed runtime opens on its installs, decided once the stream has them and no tab was asked for
  let opened = false;
  $effect(() => {
    if (opened || !live.ready || page.url.searchParams.get('tab')) return;
    opened = true;
    if (installsOf(id).length) tab.value = 'installs';
  });

  let method = $state('');
  let settings = $state<Record<string, string>>({});
  let installing = $state(false);
  const option = $derived(options.find((o) => o.method?.id === method));

  // The three things a method can do, in plain words
  const hows: Record<string, { label: string; icon: Component<any> }> = {
    adopt: { label: 'Use a binary on this host', icon: FolderInput },
    prebuilt: { label: 'Download a release', icon: Download },
    recipe: { label: 'Build from source', icon: Hammer }
  };
  const howOf = (m: InstallMethod | undefined) => hows[m?.how.case ?? ''] ?? { label: m?.id ?? '', icon: Download };

  // The first method the host can carry out is offered first
  $effect(() => {
    if (!id || (method && options.some((o) => o.method?.id === method))) return;
    method = (options.find((o) => o.unmet.length === 0) ?? options[0])?.method?.id ?? '';
  });
  // Settings belong to one method
  $effect(() => {
    void method;
    untrack(() => (settings = {}));
  });

  async function install() {
    if (!manifest || !method) return;
    installing = true;
    try {
      const r = await api.runtimes.install({ runtimeId: id, method, settings });
      startedTask(`Installing ${manifest.name || id}`, `Installed ${manifest.name || id}`, howOf(option?.method).label, r.task);
      tab.value = 'installs';
    } catch (err) {
      fail(err, 'Install refused');
    } finally {
      installing = false;
    }
  }

  async function removeInstall(installId: string) {
    const used = [...live.instances.values()].some((i) => i.installId === installId && instanceLive(i));
    const yes = await confirm({ title: `Remove this ${manifest?.name ?? id} install?`, message: used ? 'A running model uses it. It keeps running but cannot be relaunched.' : 'Downloaded files are deleted. Adopted binaries are left alone.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.runtimes.removeInstall({ id: installId });
      ok('Install removed');
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }

  async function removeBuild(buildId: string) {
    const yes = await confirm({ title: 'Remove this build?', message: 'The build directory and its install are deleted.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.builds.removeBuild({ id: buildId });
      ok('Build removed');
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }

  function range(p: { min: number; max: number; step: number }): string {
    const parts: string[] = [];
    if (p.min !== 0 || p.max !== 0) parts.push(p.max ? `${p.min} to ${p.max}` : `≥ ${p.min}`);
    if (p.step) parts.push(`step ${p.step}`);
    return parts.join(', ') || '–';
  }
</script>

{#if !cached.loaded}
  <div class="skeleton h-40" aria-busy="true"></div>
{:else if !status || !manifest}
  <PageHeader title="Runtime not found" back={{ href: '/runtimes', label: 'Runtimes' }} />
  <Empty title="No runtime manifest named {id}">
    <Button href="/runtimes">Back to Runtimes</Button>
  </Empty>
{:else}
  <PageHeader title={manifest.name || id} back={{ href: '/runtimes', label: 'Runtimes' }}>
    {#snippet meta()}
      {#if task}
        <State tone="accent" pulse label={task.kind === 'build' ? 'Building' : 'Installing'} />
      {:else if installs.length}
        <span title="Newest install added {when(installs[0].createdAt)}"><State tone="ok" label={installs[0].version ? `Installed ${installs[0].version}` : 'Installed'} /></span>
      {:else if !status.compatible}
        <State tone="warn" label="Not compatible" />
      {:else}
        <State tone="neutral" label="Not installed" />
      {/if}
      <span>{manifest.description}</span>
    {/snippet}
    {#snippet below()}
      <Tabs tabs={tabs.map((t) => (t.id === 'installs' ? { ...t, count: installs.length || undefined } : t.id === 'builds' ? { ...t, count: builds.length || undefined } : t.id === 'params' ? { ...t, count: manifest.params.length || undefined } : t))} bind:value={tab.value} />
    {/snippet}
  </PageHeader>

  {#if tab.value === 'install'}
    <div class="flex flex-col gap-5">
      {#if task}
        <div class="card flex items-center gap-3 px-4 py-3">
          <TaskChip {task} />
          <Button size="sm" variant="ghost" icon={ScrollText} class="ml-auto" href="/tasks/{task.id}">Log</Button>
        </div>
      {/if}
      {#if !status.compatible}
        <div class="note note-warn">This host lacks {status.unmet.join(', ')}.</div>
      {:else if options.length === 0}
        <Empty compact title="The manifest lists no way to install {manifest.name}" />
      {:else}
        <Card title="Method">
          <Choices
            label="Method"
            bind:value={method}
            items={options.map((o) => ({ id: o.method?.id ?? '', label: howOf(o.method).label, detail: o.unmet[0] ?? o.method?.description ?? '', warn: o.unmet.length > 0 }))}
          />
        </Card>
        {#if option}
          {#if option.unmet.length}
            <ul class="note note-warn list-disc pl-6">
              {#each option.unmet as u (u)}<li>{u}</li>{/each}
            </ul>
          {/if}
          {#if option.recipe?.recipe}
            {@const rc = option.recipe}
            <Card title="Recipe" meta={rc.recipe?.id}>
              {#if rc.recipe?.description}<p class="mb-3 text-sm text-fg-muted">{rc.recipe.description}</p>{/if}
              <Kv
                columns={2}
                items={[
                  ['Source', rc.recipe?.source?.releases || rc.recipe?.source?.repo || rc.recipe?.source?.archive || undefined],
                  ['Tools', rc.recipe?.tools.join(', ') || undefined],
                  ['Sandbox', `${enumLabel(SandboxKind, rc.sandbox)}${rc.sandboxCli ? ` through ${rc.sandboxCli}` : ''}`],
                  ['Steps', String(rc.recipe?.steps.length ?? 0)]
                ]}
              />
              {#if rc.recipe?.variants.length}
                <table class="tbl mt-3">
                  <thead><tr><th></th><th>Variant</th><th>Description</th><th>Tools</th></tr></thead>
                  <tbody>
                    {#each rc.recipe.variants as v (v.id)}
                      <tr>
                        <td class="w-6">{#if v.id === (settings.variant || rc.variant)}<Check size={13} class="text-accent" />{/if}</td>
                        <td class="font-mono text-xs text-fg">{v.id}</td>
                        <td class="text-fg-muted">{v.description || '–'}</td>
                        <td class="font-mono text-xs text-fg-faint">{v.tools.join(' ') || '–'}</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              {/if}
            </Card>
          {/if}
          {#if option.fields.length}
            <Card title="Settings">
              <ConfigForm fields={option.fields} bind:values={settings} idPrefix="inst" />
            </Card>
          {/if}
          <div class="flex justify-end">
            <Button variant="primary" icon={howOf(option.method).icon} loading={installing} disabled={!method || !!task} onclick={install}>{howOf(option.method).label}</Button>
          </div>
        {/if}
      {/if}
    </div>
  {:else if tab.value === 'installs'}
    {#if installs.length === 0}
      <Empty title="{manifest.name} is not installed">
        {#if status.compatible && options.length}<Button variant="primary" icon={Download} onclick={() => (tab.value = 'install')}>Install</Button>{/if}
      </Empty>
    {:else}
      <div class="flex flex-col gap-3">
        {#each installs as i (i.id)}
          <Card>
            <div class="flex flex-wrap items-center gap-3">
              <span class="font-mono text-sm text-fg">{i.version || 'Unknown version'}</span>
              <span class="text-xs text-fg-faint">{enumLabel(InstallKind, i.kind)} · added {ago(i.createdAt, clock.now)}</span>
              <span class="ml-auto"><Button size="sm" variant="ghost" icon={Trash2} class="text-bad hover:text-bad" onclick={() => removeInstall(i.id)}>Remove</Button></span>
            </div>
            <Kv class="mt-3" mono omitEmpty items={[['Path', i.path], ['Directory', i.dir], ['Origin', i.origin], ['Build', i.buildId || undefined]]} />
            {#if Object.keys(i.facts).length}<div class="mt-3"><ParamList params={i.facts} /></div>{/if}
          </Card>
        {/each}
      </div>
    {/if}
  {:else if tab.value === 'builds'}
    {#if builds.length === 0}
      <Empty compact title="No builds of {manifest.name} on this host" />
    {:else}
      <div class="flex flex-col gap-3">
        {#each builds as b (b.id)}
          <Card>
            <div class="flex flex-wrap items-center gap-3">
              <span class="font-mono text-sm text-fg">{b.variant}</span>
              <span class="font-mono text-xs text-fg-muted">{b.ref || '–'}{#if b.commit}<span class="text-fg-faint"> @ {b.commit.slice(0, 8)}</span>{/if}</span>
              <State values={BuildState} value={b.state} />
              <span class="text-xs text-fg-faint" title={when(b.createdAt)}>{ago(b.createdAt, clock.now)}</span>
              <span class="ml-auto flex items-center gap-1">
                {#if b.taskId}<Button size="sm" variant="ghost" icon={ScrollText} href="/tasks/{b.taskId}">Log</Button>{/if}
                <Button size="sm" variant="ghost" icon={Trash2} class="text-bad hover:text-bad" onclick={() => removeBuild(b.id)}>Remove</Button>
              </span>
            </div>
            {#if b.error}<div class="note note-bad mt-3">{b.error}</div>{/if}
            <Kv
              class="mt-3"
              mono
              omitEmpty
              columns={2}
              items={[
                ['Recipe', b.recipeId],
                ['Sandbox', `${enumLabel(SandboxKind, b.sandbox)}${b.image ? ` ${b.image}` : ''}`],
                ['Directory', b.dir],
                ['Binary', b.binary || undefined],
                ['Install', b.installId || undefined],
                ['Finished', b.finishedAt ? when(b.finishedAt) : undefined]
              ]}
            />
            {#if Object.keys(b.vars).length}<div class="mt-3"><div class="caps mb-1 text-fg-faint">Variables</div><ParamList params={b.vars} /></div>{/if}
            {#if Object.keys(b.facts).length}<div class="mt-3"><div class="caps mb-1 text-fg-faint">Host facts</div><ParamList params={b.facts} /></div>{/if}
            {#if b.patches.length}<div class="mt-3"><div class="caps mb-1 text-fg-faint">Patches</div><span class="font-mono text-xs text-fg-muted">{b.patches.join(' ')}</span></div>{/if}
          </Card>
        {/each}
      </div>
    {/if}
  {:else if tab.value === 'params'}
    {#if manifest.params.length === 0}
      <Empty compact title="{manifest.name} takes no parameters" />
    {:else}
      <div class="overflow-x-auto">
          <table class="tbl">
            <thead><tr><th>Parameter</th><th>Name</th><th>Type</th><th>Default</th><th>Range</th><th>Flag</th><th>Description</th></tr></thead>
            <tbody>
              {#each manifest.params as p (p.name)}
                <tr>
                  <td class="whitespace-nowrap text-fg">{p.label || p.name}{#if p.advanced}<span class="ml-1.5 text-xs text-fg-faint">advanced</span>{/if}</td>
                  <td class="font-mono text-xs text-fg-muted">{p.name}</td>
                  <td class="text-fg-muted">{enumLabel(ParamType, p.type)}{#if p.unit}<span class="text-fg-faint"> · {p.unit}</span>{/if}</td>
                  <td class="font-mono text-xs text-fg-muted">{p.solved ? 'auto' : p.default || '–'}{#if p.choices.length}<span class="block text-fg-faint" title={p.choices.join(', ')}>{p.choices.filter(Boolean).join(' ')}</span>{/if}</td>
                  <td class="text-xs text-fg-muted whitespace-nowrap">{range(p)}</td>
                  <td class="font-mono text-xs text-fg-faint">{p.flag || p.env || '–'}</td>
                  <td class="max-w-md text-xs leading-5 text-fg-muted">{p.description}</td>
                </tr>
              {/each}
            </tbody>
          </table>
      </div>
    {/if}
  {:else if tab.value === 'manifest'}
    <div class="grid grid-cols-1 gap-5 xl:grid-cols-2">
      <Card title="Launch">
        <Kv
          items={[
            ['Id', manifest.id],
            ['API', enumLabel(ApiFlavor, manifest.launch?.api)],
            ['Reads', manifest.formats.join(', ')],
            ['Command', manifest.launch?.command],
            ['Health check', manifest.launch?.health?.path],
            ['Stop grace', manifest.launch?.stopGraceMs ? `${manifest.launch.stopGraceMs / 1000} s` : undefined],
            ['Prepares', manifest.launch?.prepare?.formats.join(', ') || undefined],
            ['Triage rules', manifest.triage.join(', ') || undefined]
          ]}
        />
      </Card>
      <Card title="Host requirements">
        {#if manifest.constraints.length}
          <ul class="divide-y divide-line/70">
            {#each manifest.constraints as c (c.expr)}
              {@const met = !status.unmet.includes(c.message)}
              <li class="flex items-center gap-3 py-2">
                <State tone={met ? 'ok' : 'warn'} label={c.message} />
                <span class="ml-auto font-mono text-xs text-fg-faint">{c.expr}</span>
              </li>
            {/each}
          </ul>
        {:else}
          <p class="text-sm text-fg-faint">None.</p>
        {/if}
      </Card>
    </div>
  {/if}
{/if}
