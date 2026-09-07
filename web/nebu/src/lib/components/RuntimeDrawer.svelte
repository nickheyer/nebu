<script lang="ts">
  import type { Component } from 'svelte';
  import { untrack } from 'svelte';
  import { api } from '$lib/api';
  import { live, cached, clock, taskFor, instanceLive, installsOf } from '$lib/state.svelte';
  import { ago, enumLabel, newestFirst, when } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { ApiFlavor, InstallKind, ParamType, type InstallMethod } from '$proto/runtime_pb';
  import { BuildState, SandboxKind } from '$proto/recipe_pb';
  import { Check, Download, FolderInput, Hammer, ScrollText, Trash2 } from '@lucide/svelte';
  import Drawer from './ui/Drawer.svelte';
  import Tabs from './ui/Tabs.svelte';
  import Kv from './ui/Kv.svelte';
  import State from './ui/State.svelte';
  import Section from './ui/Section.svelte';
  import Button from './ui/Button.svelte';
  import Choices from './ui/Choices.svelte';
  import ParamList from './ui/ParamList.svelte';
  import Empty from './ui/Empty.svelte';
  import ConfigForm from './ConfigForm.svelte';
  import TaskChip from './TaskChip.svelte';

  // One runtime: what it is, how to install it here, the knobs it takes, and the installs and builds it has
  let { id = $bindable(''), tab = $bindable('overview'), onTask }: { id?: string; tab?: string; onTask?: (taskId: string) => void } = $props();

  const status = $derived(id ? cached.runtimes.find((r) => r.manifest?.id === id) : undefined);
  const manifest = $derived(status?.manifest);
  const installs = $derived(installsOf(id));
  const builds = $derived([...live.builds.values()].filter((b) => b.runtimeId === id).sort(newestFirst((b) => b.createdAt)));
  const task = $derived(taskFor('install', { runtime: id }) ?? taskFor('build', { runtime: id }));
  const options = $derived(status?.installs ?? []);

  let method = $state('');
  let settings = $state<Record<string, string>>({});
  let installing = $state(false);
  const option = $derived(options.find((o) => o.method?.id === method));

  // The three things a method can do, in the words a person picks between
  const hows: Record<string, { label: string; icon: Component<any> }> = {
    adopt: { label: 'Use a binary on this host', icon: FolderInput },
    prebuilt: { label: 'Download a release', icon: Download },
    recipe: { label: 'Build from source', icon: Hammer }
  };
  const howOf = (m: InstallMethod | undefined) => hows[m?.how.case ?? ''] ?? { label: m?.id ?? '', icon: Download };

  // Another runtime starts the choice over, and the first method the host can carry out is offered first
  $effect(() => {
    void id;
    method = '';
  });
  $effect(() => {
    if (!id || (method && options.some((o) => o.method?.id === method))) return;
    method = (options.find((o) => o.unmet.length === 0) ?? options[0])?.method?.id ?? '';
  });
  // Settings belong to one method, so switching starts them over
  $effect(() => {
    void method;
    untrack(() => (settings = {}));
  });

  async function install() {
    if (!manifest || !method) return;
    installing = true;
    try {
      const r = await api.runtimes.install({ runtimeId: id, method, settings });
      ok(`Installing ${manifest.name || id}`, howOf(option?.method).label, r.task ? { href: `/tasks?id=${r.task.id}`, label: 'Task' } : undefined);
    } catch (err) {
      fail(err, 'Install refused');
    } finally {
      installing = false;
    }
  }

  async function removeInstall(installId: string) {
    const used = [...live.instances.values()].some((i) => i.installId === installId && instanceLive(i));
    const yes = await confirm({ title: `Remove this ${manifest?.name ?? id} install?`, message: used ? 'An instance runs from it. It keeps running but cannot relaunch.' : 'Downloaded files are deleted. Adopted binaries stay.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.runtimes.removeInstall({ id: installId });
      ok('Install removed');
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }

  async function removeBuild(buildId: string) {
    const yes = await confirm({ title: 'Remove this build?', message: 'The build tree and its install are deleted.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.builds.removeBuild({ id: buildId });
      ok('Build removed');
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }
</script>

<Drawer bind:id title={manifest?.name || manifest?.id || 'Runtime'} subtitle={manifest?.description || manifest?.id}>
  {#snippet header()}
    {#if status && manifest}
      <div class="flex flex-wrap items-center gap-3 text-sm text-fg-muted">
        {#if task}
          <State tone="accent" pulse label={task.kind === 'build' ? 'Building' : 'Installing'} />
        {:else if installs.length}
          <State tone="ok" label="Installed" />
        {:else if !status.compatible}
          <State tone="warn" label="Incompatible" />
        {:else}
          <State tone="neutral" label="Not installed" />
        {/if}
        <span>{enumLabel(ApiFlavor, manifest.launch?.api)} API</span>
        <span class="font-mono text-xs">{manifest.formats.join(' ')}</span>
      </div>
      <Tabs
        size="sm"
        class="mt-3"
        bind:value={tab}
        tabs={[
          { id: 'overview', label: 'Overview' },
          { id: 'install', label: 'Install' },
          { id: 'params', label: 'Parameters', count: manifest.params.length || undefined },
          { id: 'installs', label: 'Installs', count: installs.length || undefined },
          { id: 'builds', label: 'Builds', count: builds.length || undefined }
        ]}
      />
    {/if}
  {/snippet}

  <div class="px-6 py-5">
    {#if status && manifest}
      {#if tab === 'overview'}
        <div class="flex flex-col gap-6">
          {#if status.unmet.length}
            <ul class="note note-warn list-disc pl-6">
              {#each status.unmet as u (u)}<li>Needs {u}</li>{/each}
            </ul>
          {/if}
          <Section title="Manifest">
            <Kv
              columns={2}
              items={[
                ['id', manifest.id],
                ['api', enumLabel(ApiFlavor, manifest.launch?.api)],
                ['formats', manifest.formats.join(', ')],
                ['command', manifest.launch?.command],
                ['health', manifest.launch?.health?.path],
                ['stop grace', manifest.launch?.stopGraceMs ? `${manifest.launch.stopGraceMs / 1000}s` : undefined],
                ['prepares', manifest.launch?.prepare?.formats.join(', ') || undefined],
                ['triage', manifest.triage.join(', ') || undefined]
              ]}
            />
          </Section>
          {#if manifest.constraints.length}
            <Section title="Needs">
              <ul class="divide-y divide-line/70 border-y border-line">
                {#each manifest.constraints as c (c.expr)}
                  {@const met = !status.unmet.includes(c.message)}
                  <li class="flex items-center gap-3 px-1.5 py-2">
                    <State tone={met ? 'ok' : 'warn'} label={c.message} />
                    <span class="ml-auto font-mono text-xs text-fg-faint">{c.expr}</span>
                  </li>
                {/each}
              </ul>
            </Section>
          {/if}
          {#if installs.length}
            <Section title="Newest install">
              <Kv mono items={[['version', installs[0].version || 'unknown'], ['kind', enumLabel(InstallKind, installs[0].kind)], ['path', installs[0].path], ['added', when(installs[0].createdAt)]]} />
            </Section>
          {/if}
        </div>
      {:else if tab === 'install'}
        <div class="flex flex-col gap-6">
          {#if task}
            <div class="flex items-center gap-3 rounded-md border border-accent/30 bg-accent/8 px-3 py-2.5">
              <TaskChip {task} />
              {#if onTask}<Button size="sm" variant="ghost" icon={ScrollText} class="ml-auto" onclick={() => onTask(task.id)}>Log</Button>{/if}
            </div>
          {/if}
          {#if !status.compatible}
            <div class="note note-warn">This host lacks {status.unmet.join(', ')}</div>
          {:else if options.length === 0}
            <Empty compact title="The manifest names no way to install {manifest.name}" />
          {:else}
            <Section title="How">
              <Choices
                label="How"
                bind:value={method}
                items={options.map((o) => ({ id: o.method?.id ?? '', label: howOf(o.method).label, detail: o.unmet[0] ?? o.method?.description ?? '', warn: o.unmet.length > 0 }))}
              />
            </Section>
            {#if option}
              {#if option.unmet.length}
                <ul class="note note-warn list-disc pl-6">
                  {#each option.unmet as u (u)}<li>{u}</li>{/each}
                </ul>
              {/if}
              {#if option.recipe?.recipe}
                {@const rc = option.recipe}
                <Section title="Recipe" meta={rc.recipe?.id}>
                  {#if rc.recipe?.description}<p class="text-sm text-fg-muted">{rc.recipe.description}</p>{/if}
                  <Kv
                    columns={2}
                    items={[
                      ['source', rc.recipe?.source?.releases || rc.recipe?.source?.repo || rc.recipe?.source?.archive || undefined],
                      ['tools', rc.recipe?.tools.join(', ') || undefined],
                      ['sandbox', `${enumLabel(SandboxKind, rc.sandbox)}${rc.sandboxCli ? ` through ${rc.sandboxCli}` : ''}`],
                      ['steps', String(rc.recipe?.steps.length ?? 0)]
                    ]}
                  />
                  {#if rc.recipe?.variants.length}
                    <table class="tbl">
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
                </Section>
              {/if}
              {#if option.fields.length}
                <Section title="Settings">
                  <ConfigForm fields={option.fields} bind:values={settings} idPrefix="inst" />
                </Section>
              {/if}
            {/if}
          {/if}
        </div>
      {:else if tab === 'params'}
        {#if manifest.params.length === 0}
          <p class="text-sm text-fg-faint">No parameters</p>
        {:else}
          <table class="tbl">
            <thead><tr><th>Name</th><th>Type</th><th>Default</th><th>Description</th></tr></thead>
            <tbody>
              {#each manifest.params as p (p.name)}
                <tr>
                  <td class="font-mono text-xs whitespace-nowrap text-fg">{p.name}{#if p.solved}<span class="ml-1.5 font-sans text-xs text-fg-faint">auto</span>{/if}</td>
                  <td class="text-fg-muted">{enumLabel(ParamType, p.type)}</td>
                  <td class="font-mono text-xs text-fg-muted">{p.default || '–'}{#if p.choices.length}<span class="block text-fg-faint" title={p.choices.join(', ')}>{p.choices.join(' ')}</span>{/if}</td>
                  <td class="max-w-md text-xs leading-5 text-fg-muted">{p.description}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {:else if tab === 'installs'}
        {#if installs.length === 0}
          <Empty compact title="Not installed">
            {#if status.compatible && options.length}<Button size="sm" variant="primary" icon={Download} onclick={() => (tab = 'install')}>Install</Button>{/if}
          </Empty>
        {:else}
          <div class="flex flex-col gap-3">
            {#each installs as i (i.id)}
              <div class="flex flex-col gap-3 rounded-md border border-line p-4">
                <div class="flex items-center gap-3">
                  <span class="font-mono text-sm text-fg">{i.version || 'unknown version'}</span>
                  <span class="text-xs text-fg-faint">{enumLabel(InstallKind, i.kind)} · added {ago(i.createdAt, clock.now)}</span>
                  <span class="ml-auto"><Button size="sm" variant="ghost" icon={Trash2} class="text-bad hover:text-bad" onclick={() => removeInstall(i.id)}>Remove</Button></span>
                </div>
                <Kv mono items={[['path', i.path], ['dir', i.dir], ['origin', i.origin], ['build', i.buildId || undefined]]} />
                {#if Object.keys(i.facts).length}<ParamList params={i.facts} />{/if}
              </div>
            {/each}
          </div>
        {/if}
      {:else if tab === 'builds'}
        {#if builds.length === 0}
          <Empty compact title="No builds" />
        {:else}
          <div class="flex flex-col gap-3">
            {#each builds as b (b.id)}
              <div class="flex flex-col gap-3 rounded-md border border-line p-4">
                <div class="flex flex-wrap items-center gap-3">
                  <span class="font-mono text-sm text-fg">{b.variant}</span>
                  <span class="font-mono text-xs text-fg-muted">{b.ref || '–'}{#if b.commit}<span class="text-fg-faint"> @ {b.commit.slice(0, 8)}</span>{/if}</span>
                  <State values={BuildState} value={b.state} />
                  <span class="text-xs text-fg-faint" title={when(b.createdAt)}>{ago(b.createdAt, clock.now)}</span>
                  <span class="ml-auto flex items-center gap-1">
                    {#if b.taskId && onTask}<Button size="sm" variant="ghost" icon={ScrollText} onclick={() => onTask(b.taskId)}>Log</Button>{/if}
                    <Button size="sm" variant="ghost" icon={Trash2} class="text-bad hover:text-bad" onclick={() => removeBuild(b.id)}>Remove</Button>
                  </span>
                </div>
                {#if b.error}<div class="note note-bad">{b.error}</div>{/if}
                <Kv
                  mono
                  columns={2}
                  items={[
                    ['recipe', b.recipeId],
                    ['sandbox', `${enumLabel(SandboxKind, b.sandbox)}${b.image ? ` ${b.image}` : ''}`],
                    ['dir', b.dir],
                    ['binary', b.binary || undefined],
                    ['install', b.installId || undefined],
                    ['finished', b.finishedAt ? when(b.finishedAt) : undefined]
                  ]}
                />
                {#if Object.keys(b.vars).length}<div><div class="caps mb-1 text-fg-faint">Vars</div><ParamList params={b.vars} /></div>{/if}
                {#if Object.keys(b.facts).length}<div><div class="caps mb-1 text-fg-faint">Host facts</div><ParamList params={b.facts} /></div>{/if}
                {#if b.patches.length}<div><div class="caps mb-1 text-fg-faint">Patches</div><span class="font-mono text-xs text-fg-muted">{b.patches.join(' ')}</span></div>{/if}
              </div>
            {/each}
          </div>
        {/if}
      {/if}
    {/if}
  </div>

  {#snippet footer()}
    {#if status?.compatible && options.length}
      {#if tab === 'install'}
        <Button variant="ghost" size="sm" onclick={() => (tab = 'overview')}>Back</Button>
        <span class="ml-auto"><Button variant="primary" size="sm" icon={howOf(option?.method).icon} loading={installing} disabled={!method || !!task} onclick={install}>Install</Button></span>
      {:else}
        <span class="ml-auto"><Button variant={installs.length ? 'secondary' : 'primary'} size="sm" icon={Download} onclick={() => (tab = 'install')}>Install</Button></span>
      {/if}
    {:else if status && !status.compatible}
      <span class="text-sm text-warn">Needs {status.unmet.join(', ')}</span>
    {/if}
  {/snippet}
</Drawer>
