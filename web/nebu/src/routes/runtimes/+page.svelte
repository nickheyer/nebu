<script lang="ts">
  import { api, message } from '$lib/api';
  import { Code, ConnectError } from '@connectrpc/connect';
  import { live, cached, refreshCached, clock, taskFor, instanceLive, profilesOf } from '$lib/state.svelte';
  import { ago, enumLabel, newestFirst, when } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { selectionParam } from '$lib/selection.svelte';
  import type { Profile, RuntimeStatus } from '$proto/runtime_pb';
  import { InstallKind } from '$proto/runtime_pb';
  import { BuildState, SandboxKind, type RecipeStatus } from '$proto/recipe_pb';
  import { Download, Hammer, FolderInput, Trash2, ScrollText, RefreshCw, Plus, Pencil, Star, StarOff, Cpu, PanelRight } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Pill from '$lib/components/ui/Pill.svelte';
  import StatePill from '$lib/components/ui/StatePill.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import Dialog from '$lib/components/ui/Dialog.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import ParamList from '$lib/components/ui/ParamList.svelte';
  import SkeletonRows from '$lib/components/ui/SkeletonRows.svelte';
  import BuildDialog from '$lib/components/BuildDialog.svelte';
  import ProfileDialog from '$lib/components/ProfileDialog.svelte';
  import RuntimeDrawer from '$lib/components/RuntimeDrawer.svelte';
  import TaskDrawer from '$lib/components/TaskDrawer.svelte';
  import TaskChip from '$lib/components/TaskChip.svelte';

  let recipes = $state<RecipeStatus[]>([]);
  let adoptOpen = $state(false);
  let adoptRuntime = $state('');
  let adoptPath = $state('');
  let adopting = $state(false);
  let buildOpen = $state(false);
  let buildRecipe = $state<RecipeStatus | null>(null);
  let profileOpen = $state(false);
  let profileEditing = $state<Profile | null>(null);
  let profileRuntime = $state('');
  let taskId = $state('');
  const sel = selectionParam('/runtimes');

  const loading = $derived(!live.ready && !live.error);
  const runtimes = $derived(cached.runtimes);
  const installs = $derived([...live.installs.values()].sort(newestFirst((i) => i.createdAt)));
  const builds = $derived([...live.builds.values()].sort(newestFirst((b) => b.createdAt)));
  const profiles = $derived(profilesOf(''));

  // Recipes have no events, so a probe and the refresh button reread them
  async function loadRecipes() {
    try {
      recipes = (await api.builds.listRecipes({})).recipes;
    } catch (err) {
      fail(err, 'Could not list recipes');
    }
  }
  $effect(() => {
    void live.host?.probedAt;
    loadRecipes();
  });
  function refresh() {
    void refreshCached();
    loadRecipes();
  }

  function installsOf(id: string) {
    return installs.filter((i) => i.runtimeId === id);
  }
  function recipesOf(id: string) {
    return recipes.filter((r) => r.recipe?.runtimeId === id);
  }
  function adoptNames(rt: RuntimeStatus | undefined): string {
    return rt?.manifest?.acquire?.adopt.join(' ') || 'binary';
  }

  function openAdopt(id: string) {
    adoptRuntime = id;
    adoptPath = '';
    adoptOpen = true;
  }
  function openBuild(rs: RecipeStatus) {
    buildRecipe = rs;
    buildOpen = true;
  }
  function newProfile(runtime = '') {
    profileEditing = null;
    profileRuntime = runtime;
    profileOpen = true;
  }
  function editProfile(p: Profile) {
    profileEditing = p;
    profileRuntime = p.runtimeId;
    profileOpen = true;
  }

  async function setDefault(p: Profile, on: boolean) {
    try {
      await api.runtimes.updateProfile({ profile: { ...p, default: on } });
      ok(on ? `${p.name} is the ${p.runtimeId} default` : `${p.name} is no longer the default`);
    } catch (err) {
      fail(err, 'Update failed');
    }
  }

  async function removeProfile(p: Profile) {
    const yes = await confirm({ title: `Remove ${p.name}?`, action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.runtimes.deleteProfile({ id: p.id });
      ok('Profile removed');
    } catch (err) {
      if (!(err instanceof ConnectError && err.code === Code.FailedPrecondition)) {
        fail(err, 'Remove failed');
        return;
      }
      // The daemon names what starts from the profile, clearing them is the person's call
      const force = await confirm({ title: `Remove ${p.name} and clear what names it?`, message: message(err), action: 'Remove all', tone: 'bad' });
      if (!force) return;
      try {
        await api.runtimes.deleteProfile({ id: p.id, force: true });
        ok('Profile removed');
      } catch (again) {
        fail(again, 'Remove failed');
      }
    }
  }

  async function adopt() {
    adopting = true;
    try {
      const r = await api.runtimes.adoptInstall({ runtimeId: adoptRuntime, path: adoptPath.trim() });
      ok(`Adopted ${adoptRuntime}`, r.install ? `${r.install.version || 'unknown version'} at ${r.install.path}` : undefined);
      adoptOpen = false;
    } catch (err) {
      fail(err, 'Adopt failed');
    } finally {
      adopting = false;
    }
  }

  async function prebuilt(runtimeId: string) {
    try {
      const r = await api.runtimes.installPrebuilt({ runtimeId });
      ok(`Downloading ${runtimeId}`, undefined, r.task ? { href: `/tasks?id=${r.task.id}`, label: 'Task' } : undefined);
    } catch (err) {
      fail(err, 'Install refused');
    }
  }

  async function removeInstall(id: string, runtimeId: string) {
    const used = [...live.instances.values()].some((i) => i.installId === id && instanceLive(i));
    const yes = await confirm({ title: `Remove this ${runtimeId} install?`, message: used ? 'An instance runs from it. It keeps running but cannot relaunch.' : 'Downloaded files are deleted. Adopted binaries stay.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.runtimes.removeInstall({ id });
      ok('Install removed');
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }

  async function removeBuild(id: string) {
    const yes = await confirm({ title: 'Remove this build?', message: 'The build tree and its install are deleted.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.builds.removeBuild({ id });
      ok('Build removed');
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }
</script>

{#snippet profileHead()}
  <thead><tr><th>Runtime</th><th>Name</th><th>Parameters</th><th>Updated</th><th></th></tr></thead>
{/snippet}

{#snippet installHead()}
  <thead><tr><th>Runtime</th><th>Kind</th><th>Version</th><th>Path</th><th>Facts</th><th>Added</th><th></th></tr></thead>
{/snippet}

{#snippet buildHead()}
  <thead><tr><th>Runtime</th><th>Variant</th><th>Ref</th><th>State</th><th>Sandbox</th><th>Started</th><th></th></tr></thead>
{/snippet}

<PageHeader title="Runtimes" subtitle="Each needs an install before it can run anything: a prebuilt release, a build from its recipe, or a binary already on this host">
  <Button icon={RefreshCw} onclick={refresh} aria-label="Reread manifests and recipes" title="Reread manifests and recipes" />
  <Button icon={Plus} onclick={() => newProfile()} disabled={!runtimes.length}>New profile</Button>
</PageHeader>

<div class="flex flex-col gap-8">
  <section>
    {#if !cached.loaded}
      <div class="grid gap-4 md:grid-cols-2">
        {#each [0, 1, 2, 3] as i (i)}<div class="card h-44" aria-busy="true"></div>{/each}
      </div>
    {:else if cached.error && runtimes.length === 0}
      <div class="card">
        <Empty title="Runtimes unavailable" description={cached.error}>
          <Button size="sm" icon={RefreshCw} onclick={refresh}>Retry</Button>
        </Empty>
      </div>
    {:else if runtimes.length === 0}
      <div class="card"><Empty icon={Cpu} title="No runtime manifests" /></div>
    {:else}
      <div class="grid gap-4 md:grid-cols-2">
        {#each runtimes as rt (rt.manifest?.id)}
          {@const id = rt.manifest?.id ?? ''}
          {@const have = installsOf(id)}
          {@const newest = have[0]}
          {@const owned = profilesOf(id).length}
          {@const recipeList = recipesOf(id)}
          {@const canPrebuilt = !!rt.manifest?.acquire?.prebuilt.length}
          {@const task = taskFor('install', { runtime: id }) ?? taskFor('build', { runtime: id })}
          <article class="card flex flex-col gap-4 p-5 transition-colors hover:border-line-strong {sel.id === id ? 'border-accent/50' : ''}">
            <div class="flex items-start gap-3">
              <button type="button" class="min-w-0 flex-1 text-left" onclick={() => (sel.id = id)}>
                <div class="flex items-baseline gap-2">
                  <span class="text-base font-semibold text-fg hover:text-accent">{rt.manifest?.name || id}</span>
                  <span class="font-mono text-xs text-fg-faint">{id}</span>
                </div>
                {#if rt.manifest?.description}<div class="mt-0.5 line-clamp-2 text-sm text-fg-muted" title={rt.manifest.description}>{rt.manifest.description}</div>{/if}
              </button>
              {#if task}
                <Pill tone="accent" dot pulse label={task.kind === 'build' ? 'Building' : 'Downloading'} />
              {:else if have.length}
                <Pill tone="ok" dot label="Installed" />
              {:else if !rt.compatible}
                <Pill tone="warn" dot label="Incompatible" />
              {:else}
                <Pill tone="neutral" label="Not installed" />
              {/if}
            </div>

            <div class="min-h-10 text-sm">
              {#if task}
                <TaskChip {task} />
              {:else if have.length}
                <div class="text-fg">{newest?.version || 'unknown version'} <span class="text-fg-muted">· {enumLabel(InstallKind, newest?.kind)}{have.length > 1 ? ` · ${have.length} installs` : ''}</span></div>
                <div class="truncate font-mono text-xs text-fg-faint" title={newest?.path}>{newest?.path}</div>
              {:else if !rt.compatible}
                <ul class="list-disc pl-5 text-fg-muted">{#each rt.unmet as u (u)}<li>{u}</li>{/each}</ul>
              {:else}
                <div class="text-fg-muted">{canPrebuilt ? 'A prebuilt release is available for this host' : recipeList.length ? 'Built from source with a recipe' : `Adopt a ${adoptNames(rt)} binary already on this host`}</div>
              {/if}
            </div>

            <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-fg-faint">
              <span class="font-mono">{(rt.manifest?.formats ?? []).join(' ')}</span>
              {#if owned}<span>{owned} {owned === 1 ? 'profile' : 'profiles'}</span>{/if}
            </div>

            <div class="mt-auto flex items-center gap-2 border-t border-line pt-4">
              {#if !have.length && !task}
                {#if canPrebuilt}
                  <Button size="sm" variant="primary" icon={Download} onclick={() => prebuilt(id)}>Install</Button>
                {:else if recipeList.length}
                  <Button size="sm" variant="primary" icon={Hammer} onclick={() => openBuild(recipeList[0])}>Build</Button>
                {:else}
                  <Button size="sm" variant="primary" icon={FolderInput} onclick={() => openAdopt(id)}>Adopt</Button>
                {/if}
              {:else}
                <Button size="sm" icon={PanelRight} onclick={() => (sel.id = id)}>Details</Button>
              {/if}
              <span class="ml-auto">
                <Menu
                  size="sm"
                  items={[
                    { label: 'Adopt a binary', icon: FolderInput, detail: `${adoptNames(rt)} on this host`, onSelect: () => openAdopt(id) },
                    ...(canPrebuilt ? [{ label: 'Install prebuilt', icon: Download, detail: 'download a release', onSelect: () => prebuilt(id) }] : []),
                    ...recipeList.map((rs) => ({ label: `Build${rs.variant ? ` · ${rs.variant}` : ''}`, icon: Hammer, detail: 'from source with a recipe', onSelect: () => openBuild(rs) })),
                    { label: '', separator: true },
                    { label: 'New profile', icon: Plus, onSelect: () => newProfile(id) }
                  ]}
                />
              </span>
            </div>
          </article>
        {/each}
      </div>
    {/if}
  </section>

  <Card title="Profiles" description="Named parameter sets. The default applies to every run that names none" flush>
    {#if loading}
      <table class="tbl">
        {@render profileHead()}
        <tbody><SkeletonRows rows={2} cols={[{ w: 'w-20' }, { w: 'w-28', sub: true }, 'w-48', 'w-14', { w: 'w-6', num: true }]} /></tbody>
      </table>
    {:else if profiles.length === 0}
      <Empty compact title="No profiles" description="Save a set of parameters once and pick it by name when running.">
        <Button size="sm" icon={Plus} onclick={() => newProfile()} disabled={!runtimes.length}>New profile</Button>
      </Empty>
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          {@render profileHead()}
          <tbody>
            {#each profiles as p (p.id)}
              <tr>
                <td class="text-fg-muted">{p.runtimeId}</td>
                <td>
                  <div class="flex items-center gap-2">
                    <span class="font-mono text-sm text-fg">{p.name}</span>
                    {#if p.default}<Pill tone="accent" label="default" />{/if}
                  </div>
                  {#if p.description}<div class="text-xs text-fg-faint">{p.description}</div>{/if}
                </td>
                <td class="max-w-md">
                  {#if Object.keys(p.params).length}<ParamList params={p.params} />{:else}<span class="text-xs text-fg-faint">runtime defaults</span>{/if}
                </td>
                <td class="text-fg-muted" title={when(p.updatedAt)}>{ago(p.updatedAt, clock.now)}</td>
                <td class="actions">
                  <Menu
                    size="sm"
                    items={[
                      { label: 'Edit', icon: Pencil, onSelect: () => editProfile(p) },
                      p.default ? { label: 'Clear default', icon: StarOff, onSelect: () => setDefault(p, false) } : { label: 'Make default', icon: Star, onSelect: () => setDefault(p, true) },
                      { label: '', separator: true },
                      { label: 'Remove', icon: Trash2, tone: 'bad', onSelect: () => removeProfile(p) }
                    ]}
                  />
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Card>

  <Card title="Installs" flush>
    {#if loading}
      <table class="tbl">
        {@render installHead()}
        <tbody><SkeletonRows rows={2} cols={[{ w: 'w-20' }, 'w-14', 'w-16', 'w-56', 'w-40', 'w-14', { w: 'w-6', num: true }]} /></tbody>
      </table>
    {:else if installs.length === 0}
      <Empty compact title="No installs" />
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          {@render installHead()}
          <tbody>
            {#each installs as i (i.id)}
              <tr>
                <td class="font-medium text-fg">{i.runtimeId}</td>
                <td class="text-fg-muted">{enumLabel(InstallKind, i.kind)}</td>
                <td class="font-mono text-xs">{i.version || '–'}</td>
                <td class="max-w-xs truncate font-mono text-xs text-fg-muted" title={i.path}>{i.path}</td>
                <td class="max-w-sm"><ParamList params={i.facts} max={4} /></td>
                <td class="text-fg-muted" title={when(i.createdAt)}>{ago(i.createdAt, clock.now)}</td>
                <td class="actions">
                  <Menu size="sm" items={[{ label: 'Remove', icon: Trash2, tone: 'bad', onSelect: () => removeInstall(i.id, i.runtimeId) }]} />
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Card>

  <Card title="Builds" flush>
    <div id="builds"></div>
    {#if loading}
      <table class="tbl">
        {@render buildHead()}
        <tbody><SkeletonRows rows={2} cols={[{ w: 'w-20', sub: true }, 'w-16', 'w-24', 'w-16', 'w-20', 'w-14', { w: 'w-16', num: true }]} /></tbody>
      </table>
    {:else if builds.length === 0}
      <Empty compact title="No builds" />
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          {@render buildHead()}
          <tbody>
            {#each builds as b (b.id)}
              <tr>
                <td>
                  <div class="font-medium text-fg">{b.runtimeId}</div>
                  <div class="font-mono text-xs text-fg-faint">{b.recipeId} · {b.id.slice(0, 12)}</div>
                </td>
                <td class="text-fg-muted">{b.variant}</td>
                <td class="font-mono text-xs">{b.ref || '–'}{#if b.commit}<span class="text-fg-faint"> @ {b.commit.slice(0, 8)}</span>{/if}</td>
                <td>
                  <StatePill values={BuildState} value={b.state} />
                  {#if b.error}<div class="mt-1 max-w-xs truncate text-xs text-bad" title={b.error}>{b.error}</div>{/if}
                </td>
                <td class="text-fg-muted">{enumLabel(SandboxKind, b.sandbox)}{b.image ? ` · ${b.image}` : ''}</td>
                <td class="text-fg-muted" title={when(b.createdAt)}>{ago(b.createdAt, clock.now)}</td>
                <td class="actions">
                  <span class="inline-flex items-center gap-1">
                    {#if b.taskId}<Button size="sm" variant="ghost" icon={ScrollText} onclick={() => (taskId = b.taskId)}>Log</Button>{/if}
                    <Menu size="sm" items={[{ label: 'Remove', icon: Trash2, tone: 'bad', onSelect: () => removeBuild(b.id) }]} />
                  </span>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Card>
</div>

<Dialog bind:open={adoptOpen} title="Adopt {adoptRuntime}" description="Use a binary already on this host">
  <Field label="Path" for="adopt-path" hint="Empty finds the binary on the daemon's PATH">
    <input id="adopt-path" class="input font-mono" bind:value={adoptPath} placeholder={adoptNames(runtimes.find((r) => r.manifest?.id === adoptRuntime))} autocomplete="off" spellcheck="false" />
  </Field>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (adoptOpen = false)}>Cancel</Button>
    <Button variant="primary" icon={FolderInput} loading={adopting} onclick={adopt}>Adopt</Button>
  {/snippet}
</Dialog>

<BuildDialog bind:open={buildOpen} recipe={buildRecipe} />
<ProfileDialog bind:open={profileOpen} editing={profileEditing} runtimeId={profileRuntime} />
<RuntimeDrawer bind:id={sel.id} />
<TaskDrawer bind:id={taskId} />
