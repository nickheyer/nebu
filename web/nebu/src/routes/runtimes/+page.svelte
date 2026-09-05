<script lang="ts">
  import { api, code, message } from '$lib/api';
  import { Code } from '@connectrpc/connect';
  import { live, cached, refreshCached, clock, taskFor, instanceLive, profilesOf } from '$lib/state.svelte';
  import { ago, enumLabel, middle, newestFirst, when } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { selectionParam } from '$lib/selection.svelte';
  import type { Profile, RuntimeStatus } from '$proto/runtime_pb';
  import { InstallKind } from '$proto/runtime_pb';
  import { BuildState, SandboxKind, type RecipeStatus } from '$proto/recipe_pb';
  import { Download, Hammer, FolderInput, Trash2, ScrollText, RefreshCw, Plus, Pencil, Star, StarOff, Cpu, PanelRight } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import State from '$lib/components/ui/State.svelte';
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
      if (code(err) !== Code.FailedPrecondition) {
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

<PageHeader title="Runtimes">
  <IconButton icon={RefreshCw} label="Reread manifests and recipes" variant="secondary" onclick={refresh} />
</PageHeader>

<div class="flex flex-col gap-9">
  <section>
    {#if !cached.loaded}
      <div class="flex flex-col gap-1.5" aria-busy="true">
        {#each [0, 1, 2, 3] as i (i)}<div class="skeleton h-16"></div>{/each}
      </div>
    {:else if cached.error && runtimes.length === 0}
      <Empty title={cached.error}>
        <Button size="sm" icon={RefreshCw} onclick={refresh}>Retry</Button>
      </Empty>
    {:else if runtimes.length === 0}
      <Empty icon={Cpu} title="No runtime manifests" />
    {:else}
      <div class="flex flex-col gap-1.5">
        {#each runtimes as rt (rt.manifest?.id)}
          {@const id = rt.manifest?.id ?? ''}
          {@const have = installsOf(id)}
          {@const newest = have[0]}
          {@const owned = profilesOf(id).length}
          {@const recipeList = recipesOf(id)}
          {@const canPrebuilt = !!rt.manifest?.acquire?.prebuilt.length}
          {@const task = taskFor('install', { runtime: id }) ?? taskFor('build', { runtime: id })}
          {@const rail = task ? 'bg-accent' : have.length ? 'bg-ok' : !rt.compatible ? 'bg-warn' : 'bg-line-strong'}
          <article class="bay grid grid-cols-[minmax(8rem,12rem)_minmax(0,1fr)_auto] items-center gap-x-6 py-3 pr-2 pl-4 {sel.id === id ? 'ring-1 ring-accent/40' : ''}">
            <span class="absolute inset-y-2.5 left-0 w-0.5 rounded-full {rail}"></span>
            <button type="button" class="min-w-0 text-left" onclick={() => (sel.id = id)}>
              <div class="truncate text-sm font-semibold text-fg">{rt.manifest?.name || id}</div>
              <div class="mt-1">
                {#if task}
                  <State tone="accent" pulse label={task.kind === 'build' ? 'Building' : 'Downloading'} />
                {:else if have.length}
                  <State tone="ok" label="Installed" />
                {:else if !rt.compatible}
                  <State tone="warn" label="Incompatible" />
                {:else}
                  <State tone="neutral" label="Not installed" />
                {/if}
              </div>
            </button>
            <button type="button" class="min-w-0 text-left" onclick={() => (sel.id = id)}>
              {#if task}
                <TaskChip {task} />
              {:else if have.length}
                <div class="truncate text-sm text-fg">{newest?.version || 'unknown version'} <span class="text-fg-muted">· {enumLabel(InstallKind, newest?.kind)}{have.length > 1 ? ` · ${have.length} installs` : ''}</span></div>
                <div class="mt-1 truncate font-mono text-xs text-fg-faint" title={newest?.path}>{middle(newest?.path ?? '', 64)}</div>
              {:else if !rt.compatible}
                <div class="truncate text-sm text-fg-muted" title={rt.unmet.join('\n')}>{rt.unmet.join(' · ')}</div>
                <div class="mt-1 truncate text-xs text-fg-faint" title={rt.manifest?.description}>{rt.manifest?.description}</div>
              {:else}
                <div class="truncate text-sm text-fg-muted" title={rt.manifest?.description}>{rt.manifest?.description}</div>
                <div class="mt-1 truncate font-mono text-xs text-fg-faint">{(rt.manifest?.formats ?? []).join(' ')}{owned ? ` · ${owned} ${owned === 1 ? 'profile' : 'profiles'}` : ''}</div>
              {/if}
            </button>
            <div class="flex items-center gap-1">
              {#if !have.length && !task}
                {#if canPrebuilt}<Button size="sm" variant="primary" icon={Download} onclick={() => prebuilt(id)}>Install</Button>{/if}
                {#each recipeList as rs (rs.recipe?.id)}
                  <Button size="sm" variant={canPrebuilt ? 'subtle' : 'primary'} icon={Hammer} onclick={() => openBuild(rs)}>Build{rs.variant ? ` · ${rs.variant}` : ''}</Button>
                {/each}
                <Button size="sm" variant={canPrebuilt || recipeList.length ? 'subtle' : 'primary'} icon={FolderInput} onclick={() => openAdopt(id)}>Adopt</Button>
              {:else}
                <IconButton size="sm" icon={PanelRight} label="Details" onclick={() => (sel.id = id)} />
              {/if}
              <Menu
                size="sm"
                items={[
                  { label: 'Adopt a binary', icon: FolderInput, detail: `${adoptNames(rt)} on this host`, onSelect: () => openAdopt(id) },
                  ...(canPrebuilt ? [{ label: 'Install prebuilt', icon: Download, detail: 'download a release', onSelect: () => prebuilt(id) }] : []),
                  ...recipeList.map((rs) => ({ label: `Build${rs.variant ? ` · ${rs.variant}` : ''}`, icon: Hammer, detail: 'from source', onSelect: () => openBuild(rs) })),
                  { label: '', separator: true },
                  { label: 'New profile', icon: Plus, onSelect: () => newProfile(id) }
                ]}
              />
            </div>
          </article>
        {/each}
      </div>
    {/if}
  </section>

  <Section title="Profiles" count={profiles.length || undefined} info="Named parameter sets. The default applies to every run that names none.">
    {#snippet actions()}
      <Button size="sm" icon={Plus} onclick={() => newProfile()} disabled={!runtimes.length}>New profile</Button>
    {/snippet}
    {#if loading}
      <table class="tbl">
        {@render profileHead()}
        <tbody><SkeletonRows rows={2} cols={[{ w: 'w-20' }, { w: 'w-28', sub: true }, 'w-48', 'w-14', { w: 'w-6', num: true }]} /></tbody>
      </table>
    {:else if profiles.length === 0}
      <Empty compact title="No profiles" />
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          {@render profileHead()}
          <tbody>
            {#each profiles as p (p.id)}
              <tr>
                <td class="text-fg-muted">{p.runtimeId}</td>
                <td>
                  <span class="font-mono text-sm text-fg">{p.name}</span>
                  {#if p.default}<span class="ml-1.5 text-xs text-fg-faint">default</span>{/if}
                  {#if p.description}<div class="text-xs text-fg-faint">{p.description}</div>{/if}
                </td>
                <td class="max-w-md">
                  {#if Object.keys(p.params).length}<ParamList params={p.params} />{:else}<span class="text-xs text-fg-faint">runtime defaults</span>{/if}
                </td>
                <td class="text-fg-muted" title={when(p.updatedAt)}>{ago(p.updatedAt, clock.now)}</td>
                <td class="actions">
                  <span>
                    <IconButton size="sm" icon={Pencil} label="Edit" onclick={() => editProfile(p)} />
                    <Menu
                      size="sm"
                      items={[
                        p.default ? { label: 'Clear default', icon: StarOff, onSelect: () => setDefault(p, false) } : { label: 'Make default', icon: Star, onSelect: () => setDefault(p, true) },
                        { label: '', separator: true },
                        { label: 'Remove', icon: Trash2, tone: 'bad', onSelect: () => removeProfile(p) }
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

  <Section title="Installs" count={installs.length || undefined}>
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
                <td class="text-fg">{i.runtimeId}</td>
                <td class="text-fg-muted">{enumLabel(InstallKind, i.kind)}</td>
                <td class="font-mono text-xs">{i.version || '–'}</td>
                <td class="font-mono text-xs text-fg-muted" title={i.path}>{middle(i.path, 44)}</td>
                <td class="max-w-sm"><ParamList params={i.facts} max={3} /></td>
                <td class="text-fg-muted" title={when(i.createdAt)}>{ago(i.createdAt, clock.now)}</td>
                <td class="actions">
                  <span><Menu size="sm" items={[{ label: 'Remove', icon: Trash2, tone: 'bad', onSelect: () => removeInstall(i.id, i.runtimeId) }]} /></span>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Section>

  <Section title="Builds" count={builds.length || undefined} id="builds">
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
                  <div class="text-fg">{b.runtimeId}</div>
                  <div class="font-mono text-xs text-fg-faint">{b.recipeId} · {b.id.slice(0, 12)}</div>
                </td>
                <td class="text-fg-muted">{b.variant}</td>
                <td class="font-mono text-xs">{b.ref || '–'}{#if b.commit}<span class="text-fg-faint"> @ {b.commit.slice(0, 8)}</span>{/if}</td>
                <td>
                  <State values={BuildState} value={b.state} />
                  {#if b.error}<div class="mt-1 max-w-xs truncate text-xs text-bad" title={b.error}>{b.error}</div>{/if}
                </td>
                <td class="text-fg-muted">{enumLabel(SandboxKind, b.sandbox)}{b.image ? ` · ${b.image}` : ''}</td>
                <td class="text-fg-muted" title={when(b.createdAt)}>{ago(b.createdAt, clock.now)}</td>
                <td class="actions">
                  <span>
                    {#if b.taskId}<IconButton size="sm" icon={ScrollText} label="Log" onclick={() => (taskId = b.taskId)} />{/if}
                    <Menu size="sm" items={[{ label: 'Remove', icon: Trash2, tone: 'bad', onSelect: () => removeBuild(b.id) }]} />
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

<Dialog bind:open={adoptOpen} title="Adopt {adoptRuntime}" size="sm">
  <Field label="Path" for="adopt-path" info="Empty finds the binary on the daemon's PATH">
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
