<script lang="ts">
  import { api, message } from '$lib/api';
  import { Code, ConnectError } from '@connectrpc/connect';
  import { live, cached, refreshCached, clock, taskFor, instanceLive, profilesOf } from '$lib/state.svelte';
  import { ago, enumLabel, newestFirst, when } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import type { Profile } from '$proto/runtime_pb';
  import { InstallKind } from '$proto/runtime_pb';
  import { BuildState, SandboxKind, type RecipeStatus } from '$proto/recipe_pb';
  import { Wrench, Download, Hammer, FolderInput, Trash2, ScrollText, RefreshCw, CircleCheck, CircleAlert, Package, SlidersHorizontal, Plus, Pencil, Star, StarOff } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Panel from '$lib/components/ui/Panel.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import StateBadge from '$lib/components/ui/StateBadge.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import Dialog from '$lib/components/ui/Dialog.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import Skeleton from '$lib/components/ui/Skeleton.svelte';
  import ParamChips from '$lib/components/ui/ParamChips.svelte';
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
  let runtimeId = $state('');

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

  function newProfile(runtimeId = '') {
    profileEditing = null;
    profileRuntime = runtimeId;
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
  function recipesOf(id: string) {
    return recipes.filter((r) => r.recipe?.runtimeId === id);
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

<PageHeader title="Runtimes">
  <Button variant="outline" icon={RefreshCw} onclick={refresh}>Refresh</Button>
</PageHeader>

<div class="flex flex-col gap-6">
  <section class="grid grid-cols-1 gap-3 lg:grid-cols-2">
    {#if !cached.loaded}
      {#each [1, 2] as i (i)}<div class="panel p-4"><Skeleton rows={4} /></div>{/each}
    {:else}
      {#each runtimes as rt (rt.manifest?.id)}
        {@const id = rt.manifest?.id ?? ''}
        {@const have = installsOf(id)}
        {@const owned = profilesOf(id).length}
        {@const installing = taskFor('install', { runtime: id })}
        {@const building = taskFor('build', { runtime: id })}
        <div class="panel flex flex-col gap-3 p-4">
          <div class="flex items-start gap-3">
            <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-line bg-raised text-fg-muted"><Wrench size={16} /></div>
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-center gap-2">
                <button class="text-sm font-semibold text-fg hover:underline" onclick={() => (runtimeId = id)}>{rt.manifest?.name}</button>
                <span class="font-mono text-xs text-fg-faint">{id}</span>
                <span class="inline-flex items-center gap-1.5 text-[11px] {rt.compatible ? 'text-ok' : 'text-warn'}"><span class="h-1.5 w-1.5 rounded-full bg-current"></span>{rt.compatible ? 'compatible' : 'incompatible'}</span>
              </div>
              <p class="mt-1 text-sm leading-5 text-fg-muted">{rt.manifest?.description}</p>
            </div>
          </div>
          <div class="flex flex-wrap gap-1.5">
            <span class="font-mono text-[11px] text-fg-faint">{(rt.manifest?.formats ?? []).join(' ')}</span>
            <span class="ml-auto text-xs text-fg-faint">{have.length} {have.length === 1 ? 'install' : 'installs'} · {owned} {owned === 1 ? 'profile' : 'profiles'}</span>
          </div>
          {#if rt.unmet.length}
            <ul class="rounded-md border border-warn/30 bg-warn/8 px-3 py-2 text-xs leading-5 text-warn">
              {#each rt.unmet as u (u)}<li class="flex gap-2"><CircleAlert size={13} class="mt-1 shrink-0" />{u}</li>{/each}
            </ul>
          {/if}
          {#if installing}<TaskChip task={installing} label="Downloading release" />{/if}
          {#if building}<TaskChip task={building} label="Building" />{/if}
          <div class="mt-auto flex flex-wrap gap-2 pt-1">
            <Button
              size="sm"
              icon={FolderInput}
              onclick={() => {
                adoptRuntime = id;
                adoptPath = '';
                adoptOpen = true;
              }}>Adopt</Button
            >
            {#if rt.manifest?.acquire?.prebuilt.length}
              <Button size="sm" icon={Download} onclick={() => prebuilt(id)}>Install</Button>
            {/if}
            <Button size="sm" icon={SlidersHorizontal} onclick={() => newProfile(id)}>New profile</Button>
            {#each recipesOf(id) as rs (rs.recipe?.id)}
              <Button
                size="sm"
                icon={Hammer}
                title={rs.unmet.join('; ')}
                onclick={() => {
                  buildRecipe = rs;
                  buildOpen = true;
                }}>Build{rs.variant ? ` · ${rs.variant}` : ''}</Button
              >
            {/each}
          </div>
        </div>
      {:else}
        <div class="panel lg:col-span-2"><Empty icon={Wrench} title="No runtime manifests" /></div>
      {/each}
    {/if}
  </section>

  <Panel title="Profiles" info="Named param sets per runtime. The default applies to every run that names none" flush>
    {#snippet actions()}
      <Button size="sm" icon={Plus} onclick={() => newProfile()} disabled={!runtimes.length}>New profile</Button>
    {/snippet}
    {#if profiles.length === 0}
      <Empty compact icon={SlidersHorizontal} title="No profiles" />
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          <thead><tr><th>runtime</th><th>name</th><th>params</th><th>updated</th><th></th></tr></thead>
          <tbody>
            {#each profiles as p (p.id)}
              <tr>
                <td class="font-medium text-fg">{p.runtimeId}</td>
                <td>
                  <div class="flex items-center gap-2">
                    <span class="font-mono text-xs text-fg">{p.name}</span>
                    {#if p.default}<span class="text-[11px] text-accent">default</span>{/if}
                  </div>
                  {#if p.description}<div class="text-[11px] text-fg-faint">{p.description}</div>{/if}
                </td>
                <td class="max-w-md">
                  {#if Object.keys(p.params).length}<ParamChips params={p.params} />{:else}<span class="text-[11px] text-fg-faint">defaults</span>{/if}
                </td>
                <td class="text-xs text-fg-muted" title={when(p.updatedAt)}>{ago(p.updatedAt, clock.now)}</td>
                <td class="text-right">
                  <Menu
                    items={[
                      { label: 'Edit', icon: Pencil, onSelect: () => editProfile(p) },
                      p.default ? { label: 'Clear default', icon: StarOff, onSelect: () => setDefault(p, false) } : { label: 'Make default', icon: Star, onSelect: () => setDefault(p, true) },
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
  </Panel>

  <Panel title="Installs" flush>
    {#if installs.length === 0}
      <Empty compact icon={Package} title="No installs" />
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          <thead><tr><th>runtime</th><th>kind</th><th>version</th><th>path</th><th>facts</th><th>added</th><th></th></tr></thead>
          <tbody>
            {#each installs as i (i.id)}
              <tr>
                <td class="font-medium text-fg">{i.runtimeId}</td>
                <td class="text-xs text-fg-muted">{enumLabel(InstallKind, i.kind)}</td>
                <td class="font-mono text-xs">{i.version || '–'}</td>
                <td class="max-w-xs truncate font-mono text-xs text-fg-muted" title={i.path}>{i.path}</td>
                <td class="max-w-sm"><ParamChips params={i.facts} max={4} /></td>
                <td class="text-xs text-fg-muted" title={when(i.createdAt)}>{ago(i.createdAt, clock.now)}</td>
                <td class="text-right">
                  <Menu items={[{ label: 'Remove', icon: Trash2, tone: 'bad', onSelect: () => removeInstall(i.id, i.runtimeId) }]} />
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Panel>

  <Panel title="Builds" flush>
    <div id="builds"></div>
    {#if builds.length === 0}
      <Empty compact icon={Hammer} title="No builds" />
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          <thead><tr><th>runtime</th><th>variant</th><th>ref</th><th>state</th><th>sandbox</th><th>started</th><th></th></tr></thead>
          <tbody>
            {#each builds as b (b.id)}
              <tr>
                <td>
                  <div class="font-medium text-fg">{b.runtimeId}</div>
                  <div class="font-mono text-[11px] text-fg-faint">{b.recipeId} · {b.id.slice(0, 12)}</div>
                </td>
                <td class="text-xs">{b.variant}</td>
                <td class="font-mono text-xs">{b.ref || '–'}{#if b.commit}<span class="text-fg-faint"> @ {b.commit.slice(0, 8)}</span>{/if}</td>
                <td>
                  <StateBadge values={BuildState} value={b.state} size="xs" />
                  {#if b.error}<div class="mt-1 max-w-xs truncate text-[11px] text-bad" title={b.error}>{b.error}</div>{/if}
                </td>
                <td class="text-xs">{enumLabel(SandboxKind, b.sandbox)}{b.image ? ` · ${b.image}` : ''}</td>
                <td class="text-xs text-fg-muted" title={when(b.createdAt)}>{ago(b.createdAt, clock.now)}</td>
                <td class="text-right">
                  <span class="inline-flex items-center gap-1">
                    {#if b.taskId}<Button size="xs" variant="ghost" icon={ScrollText} onclick={() => (taskId = b.taskId)}>Log</Button>{/if}
                    <Menu items={[{ label: 'Remove', icon: Trash2, tone: 'bad', onSelect: () => removeBuild(b.id) }]} />
                  </span>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Panel>
</div>

<Dialog bind:open={adoptOpen} title="Adopt a {adoptRuntime} binary">
  <Field label="Path" for="adopt-path" hint="Empty searches PATH">
    <input id="adopt-path" class="input font-mono" bind:value={adoptPath} placeholder={runtimes.find((r) => r.manifest?.id === adoptRuntime)?.manifest?.acquire?.adopt.join(', ') || 'path to the binary'} />
  </Field>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (adoptOpen = false)}>Cancel</Button>
    <Button variant="primary" icon={CircleCheck} loading={adopting} onclick={adopt}>Adopt</Button>
  {/snippet}
</Dialog>

<BuildDialog bind:open={buildOpen} recipe={buildRecipe} />
<ProfileDialog bind:open={profileOpen} editing={profileEditing} runtimeId={profileRuntime} />
<RuntimeDrawer bind:id={runtimeId} />
<TaskDrawer bind:id={taskId} />
