<script lang="ts">
  import { api, message } from '$lib/api';
  import { Code, ConnectError } from '@connectrpc/connect';
  import { live, clock, taskFor, instanceLive } from '$lib/state.svelte';
  import { ago, enumLabel, newestFirst, when, byName } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import type { Profile, RuntimeStatus } from '$proto/runtime_pb';
  import { InstallKind } from '$proto/runtime_pb';
  import { BuildState, SandboxKind, type RecipeStatus } from '$proto/recipe_pb';
  import { Wrench, Download, Hammer, FolderInput, Trash2, ScrollText, RefreshCw, CircleCheck, CircleAlert, Package, SlidersHorizontal, Plus, Pencil, Star, StarOff } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Panel from '$lib/components/ui/Panel.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Badge from '$lib/components/ui/Badge.svelte';
  import StateBadge from '$lib/components/ui/StateBadge.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import Dialog from '$lib/components/ui/Dialog.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import Skeleton from '$lib/components/ui/Skeleton.svelte';
  import BuildDialog from '$lib/components/BuildDialog.svelte';
  import ProfileDialog from '$lib/components/ProfileDialog.svelte';
  import TaskDrawer from '$lib/components/TaskDrawer.svelte';
  import TaskChip from '$lib/components/TaskChip.svelte';

  let runtimes = $state<RuntimeStatus[]>([]);
  let recipes = $state<RecipeStatus[]>([]);
  let loaded = $state(false);
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

  const installs = $derived([...live.installs.values()].sort(newestFirst));
  const builds = $derived([...live.builds.values()].sort(newestFirst));
  const profiles = $derived([...live.profiles.values()].sort(byName((p) => `${p.runtimeId} ${p.default ? 0 : 1} ${p.name}`)));

  async function refresh() {
    try {
      const [r, c] = await Promise.all([api.runtimes.listRuntimes({}), api.builds.listRecipes({})]);
      runtimes = r.runtimes;
      recipes = c.recipes;
    } catch (err) {
      fail(err, 'Could not list runtimes');
    } finally {
      loaded = true;
    }
  }
  $effect(() => {
    void live.host?.probedAt;
    refresh();
  });

  function installsOf(id: string) {
    return installs.filter((i) => i.runtimeId === id);
  }
  function profilesOf(id: string) {
    return profiles.filter((p) => p.runtimeId === id);
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
      ok(on ? `${p.name} is the ${p.runtimeId} default` : `${p.name} is no longer the default`, on ? 'Every run that names no profile starts from it' : 'Runs fall back to the manifest defaults');
    } catch (err) {
      fail(err, 'Update failed');
    }
  }

  async function removeProfile(p: Profile) {
    const yes = await confirm({ title: `Remove ${p.name}?`, message: p.default ? `Runs of ${p.runtimeId} that name no profile fall back to the manifest defaults.` : 'Nothing that names it is touched without asking you first.', action: 'Remove', tone: 'bad' });
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
      const force = await confirm({ title: `Clear what names ${p.name}?`, message: `${message(err)}. Cleared ones start from the ${p.runtimeId} default profile instead.`, action: 'Clear and remove', tone: 'bad' });
      if (!force) return;
      try {
        await api.runtimes.deleteProfile({ id: p.id, force: true });
        ok('Profile removed', 'What named it now starts from the runtime default');
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
      ok(`Adopted ${adoptRuntime}`, r.install ? `${r.install.version || 'version unknown'} at ${r.install.path}` : undefined);
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
      ok(`Downloading ${runtimeId}`, 'The release matching this host', r.task ? { href: `/tasks?id=${r.task.id}`, label: 'Follow' } : undefined);
    } catch (err) {
      fail(err, 'Install refused');
    }
  }

  async function removeInstall(id: string, runtimeId: string) {
    const used = [...live.instances.values()].some((i) => i.installId === id && instanceLive(i));
    const yes = await confirm({ title: `Remove this ${runtimeId} install?`, message: used ? 'An instance is running from it. It keeps running but cannot be relaunched after a restart.' : 'Downloaded files are deleted. Adopted binaries are left where they are.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.runtimes.removeInstall({ id });
      ok('Install removed');
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }

  async function removeBuild(id: string) {
    const yes = await confirm({ title: 'Remove this build?', message: 'The build tree and the install it produced are deleted.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.builds.removeBuild({ id });
      ok('Build removed');
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }
</script>

<PageHeader title="Runtimes" description="Backends this host can serve with, and the installs that make them usable">
  <Button variant="outline" icon={RefreshCw} onclick={refresh}>Refresh</Button>
</PageHeader>

<div class="flex flex-col gap-6">
  <section class="grid grid-cols-1 gap-3 lg:grid-cols-2">
    {#if !loaded}
      {#each [1, 2] as i (i)}<div class="panel p-4"><Skeleton rows={4} /></div>{/each}
    {:else}
      {#each runtimes as rt (rt.manifest?.id)}
        {@const id = rt.manifest?.id ?? ''}
        {@const have = installsOf(id)}
        {@const installing = taskFor('install', { runtime: id })}
        {@const building = taskFor('build', { runtime: id })}
        <div class="panel flex flex-col gap-3 p-4">
          <div class="flex items-start gap-3">
            <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-line bg-raised text-fg-muted"><Wrench size={16} /></div>
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-center gap-2">
                <span class="text-sm font-semibold text-fg">{rt.manifest?.name}</span>
                <span class="font-mono text-xs text-fg-faint">{id}</span>
                {#if rt.compatible}
                  <Badge tone="ok" size="xs" dot label="compatible" />
                {:else}
                  <Badge tone="warn" size="xs" dot label="incompatible" />
                {/if}
              </div>
              <p class="mt-1 text-sm leading-5 text-fg-muted">{rt.manifest?.description}</p>
            </div>
          </div>
          <div class="flex flex-wrap gap-1.5">
            {#each rt.manifest?.formats ?? [] as f (f)}<span class="rounded border border-line bg-sunken px-1.5 py-0.5 font-mono text-[11px] text-fg-muted">{f}</span>{/each}
            <span class="ml-auto text-xs text-fg-faint">{have.length} {have.length === 1 ? 'install' : 'installs'} · {profilesOf(id).length} {profilesOf(id).length === 1 ? 'profile' : 'profiles'}</span>
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
              }}>Adopt binary</Button
            >
            {#if rt.manifest?.acquire?.prebuilt.length}
              <Button size="sm" icon={Download} onclick={() => prebuilt(id)}>Install prebuilt</Button>
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
        <div class="panel lg:col-span-2"><Empty icon={Wrench} title="No runtime manifests" description="Drop a manifest under a spec directory to teach nebu a backend." /></div>
      {/each}
    {/if}
  </section>

  <Panel title="Profiles" description="Named param sets per runtime, the default one applies to every run that names none" flush>
    {#snippet actions()}
      <Button size="sm" icon={Plus} onclick={() => newProfile()} disabled={!runtimes.length}>New profile</Button>
    {/snippet}
    {#if profiles.length === 0}
      <Empty compact icon={SlidersHorizontal} title="No profiles yet" description="A profile fixes the knobs of a runtime once, so a run only says which model. Mark one as the default and every run of that runtime starts from it." />
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          <thead><tr><th>runtime</th><th>name</th><th>params</th><th>updated</th><th></th></tr></thead>
          <tbody>
            {#each profiles as p (p.id)}
              {@const entries = Object.entries(p.params)}
              <tr>
                <td class="font-medium text-fg">{p.runtimeId}</td>
                <td>
                  <div class="flex items-center gap-2">
                    <span class="font-mono text-xs text-fg">{p.name}</span>
                    {#if p.default}<Badge tone="accent" size="xs" label="default" />{/if}
                  </div>
                  {#if p.description}<div class="text-[11px] text-fg-faint">{p.description}</div>{/if}
                </td>
                <td class="max-w-md">
                  <div class="flex flex-wrap gap-1">
                    {#each entries as [k, v] (k)}
                      <span class="rounded bg-sunken px-1 font-mono text-[10.5px] text-fg-faint" title="{k}={v}"><span>{k}=</span><span class="text-fg-muted">{v.length > 24 ? v.slice(0, 24) + '…' : v}</span></span>
                    {:else}
                      <span class="text-[11px] text-fg-faint">manifest defaults</span>
                    {/each}
                  </div>
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

  <Panel title="Installs" description="Usable copies of a runtime, newest first" flush>
    {#if installs.length === 0}
      <Empty compact icon={Package} title="No installs yet" description="Adopt a binary already on the host, download a prebuilt release, or build one from a recipe." />
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          <thead><tr><th>runtime</th><th>kind</th><th>version</th><th>path</th><th>facts</th><th>added</th><th></th></tr></thead>
          <tbody>
            {#each installs as i (i.id)}
              {@const facts = Object.entries(i.facts)}
              <tr>
                <td class="font-medium text-fg">{i.runtimeId}</td>
                <td><Badge size="xs" label={enumLabel(InstallKind, i.kind)} tone={i.kind === InstallKind.BUILT ? 'accent' : 'neutral'} /></td>
                <td class="font-mono text-xs">{i.version || '–'}</td>
                <td class="max-w-xs truncate font-mono text-xs text-fg-muted" title={i.path}>{i.path}</td>
                <td class="max-w-sm">
                  <div class="flex flex-wrap gap-1">
                    {#each facts.slice(0, 4) as [k, v] (k)}
                      <span class="rounded bg-sunken px-1 font-mono text-[10.5px] text-fg-faint" title="{k}={v}"><span>{k}=</span><span class="text-fg-muted">{v.length > 24 ? v.slice(0, 24) + '…' : v}</span></span>
                    {/each}
                    {#if facts.length > 4}<span class="text-[10.5px] text-fg-faint">+{facts.length - 4}</span>{/if}
                  </div>
                </td>
                <td class="text-xs text-fg-muted" title={when(i.createdAt)}>{ago(i.createdAt, clock.now)}</td>
                <td class="text-right">
                  <Menu items={[{ label: 'Remove install', icon: Trash2, tone: 'bad', onSelect: () => removeInstall(i.id, i.runtimeId) }]} />
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Panel>

  <Panel title="Builds" description="Recipe runs on this host, keyed by the hash of everything that decides their bytes" flush>
    <div id="builds"></div>
    {#if builds.length === 0}
      <Empty compact icon={Hammer} title="No builds yet" description="Building from a recipe produces an install of kind built. An unchanged recipe on an unchanged host is a cache hit." />
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
                    <Menu items={[{ label: 'Remove build and install', icon: Trash2, tone: 'bad', onSelect: () => removeBuild(b.id) }]} />
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

<Dialog bind:open={adoptOpen} title="Adopt a binary for {adoptRuntime}" description="Records a binary already on this host as an install and probes its version and devices">
  <Field label="Executable path" for="adopt-path" hint="Leave empty to search PATH for the names the manifest lists">
    <input id="adopt-path" class="input font-mono" bind:value={adoptPath} placeholder={runtimes.find((r) => r.manifest?.id === adoptRuntime)?.manifest?.acquire?.adopt.join(', ') || 'path to the binary'} />
  </Field>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (adoptOpen = false)}>Cancel</Button>
    <Button variant="primary" icon={CircleCheck} loading={adopting} onclick={adopt}>Adopt</Button>
  {/snippet}
</Dialog>

<BuildDialog bind:open={buildOpen} recipe={buildRecipe} />
<ProfileDialog bind:open={profileOpen} {runtimes} editing={profileEditing} runtimeId={profileRuntime} />
<TaskDrawer bind:id={taskId} />
