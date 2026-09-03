<script lang="ts">
  import { api, message } from '$lib/api';
  import { live, byCreated } from '$lib/state.svelte';
  import { enumName, parsePairs, when } from '$lib/format';
  import Badge from '$lib/components/Badge.svelte';
  import TaskLog from '$lib/components/TaskLog.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import type { RuntimeStatus } from '$proto/runtime_pb';
  import { InstallKind } from '$proto/runtime_pb';
  import { BuildState, SandboxKind, type RecipeStatus } from '$proto/recipe_pb';

  let runtimes = $state<RuntimeStatus[]>([]);
  let recipes = $state<RecipeStatus[]>([]);
  let error = $state('');
  let tasks = $state<string[]>([]);
  let adoptOpen = $state(false);
  let adoptRuntime = $state('');
  let adoptPath = $state('');
  let buildOpen = $state(false);
  let buildRecipe = $state<RecipeStatus | null>(null);
  let variant = $state('');
  let sandbox = $state('host');
  let image = $state('');
  let ref = $state('');
  let vars = $state('');
  let force = $state(false);

  const installs = $derived([...live.installs.values()].sort(byCreated));
  const builds = $derived([...live.builds.values()].sort(byCreated));

  async function refresh() {
    error = '';
    try {
      runtimes = (await api.runtimes.listRuntimes({})).runtimes;
      recipes = (await api.builds.listRecipes({})).recipes;
    } catch (err) {
      error = message(err);
    }
  }
  $effect(() => {
    refresh();
  });

  async function adopt() {
    error = '';
    try {
      await api.runtimes.adoptInstall({ runtimeId: adoptRuntime, path: adoptPath });
      adoptOpen = false;
    } catch (err) {
      error = message(err);
    }
  }

  async function prebuilt(runtimeId: string) {
    error = '';
    try {
      const r = await api.runtimes.installPrebuilt({ runtimeId });
      if (r.task) tasks = [r.task.id, ...tasks];
    } catch (err) {
      error = message(err);
    }
  }

  async function removeInstall(id: string) {
    if (!confirm(`remove install ${id}?`)) return;
    error = '';
    try {
      await api.runtimes.removeInstall({ id });
    } catch (err) {
      error = message(err);
    }
  }

  function openBuild(rs: RecipeStatus) {
    buildRecipe = rs;
    variant = rs.variant;
    sandbox = rs.sandbox === SandboxKind.OCI ? 'oci' : 'host';
    image = '';
    ref = '';
    vars = '';
    force = false;
    buildOpen = true;
  }

  async function build() {
    if (!buildRecipe?.recipe) return;
    error = '';
    try {
      const r = await api.builds.build({
        recipeId: buildRecipe.recipe.id,
        variant,
        sandbox: sandbox === 'oci' ? SandboxKind.OCI : SandboxKind.HOST,
        image,
        ref,
        vars: parsePairs(vars),
        force
      });
      if (r.task) tasks = [r.task.id, ...tasks];
      buildOpen = false;
    } catch (err) {
      error = message(err);
    }
  }

  async function removeBuild(id: string) {
    if (!confirm(`remove build ${id} and its install?`)) return;
    error = '';
    try {
      await api.builds.removeBuild({ id });
    } catch (err) {
      error = message(err);
    }
  }
</script>

<div class="space-y-5">
  <div class="flex items-center gap-3">
    <h1 class="h1">Runtimes</h1>
    <button class="btn ml-auto" onclick={refresh}>refresh</button>
  </div>
  {#if error}<div class="text-sm text-red-300">{error}</div>{/if}

  <div class="grid grid-cols-1 gap-3 lg:grid-cols-2">
    {#each runtimes as rt (rt.manifest?.id)}
      <div class="card">
        <div class="flex items-center gap-2">
          <span class="font-semibold">{rt.manifest?.name}</span>
          <span class="muted mono text-xs">{rt.manifest?.id}</span>
          <Badge state={rt.compatible ? 'ok' : 'warn'} />
          <span class="muted text-xs">{rt.manifest?.formats.join(', ')}</span>
        </div>
        <div class="muted mt-1 text-sm">{rt.manifest?.description}</div>
        {#if rt.unmet.length}<div class="mt-1 text-xs text-amber-300">needs {rt.unmet.join(', ')}</div>{/if}
        <div class="mt-3 flex flex-wrap gap-2">
          <button class="btn" onclick={() => { adoptRuntime = rt.manifest?.id ?? ''; adoptPath = ''; adoptOpen = true; }}>adopt binary</button>
          {#if rt.manifest?.acquire?.prebuilt.length}
            <button class="btn" onclick={() => prebuilt(rt.manifest?.id ?? '')}>install prebuilt</button>
          {/if}
          {#each recipes.filter((r) => r.recipe?.runtimeId === rt.manifest?.id) as rs (rs.recipe?.id)}
            <button class="btn" onclick={() => openBuild(rs)} title={rs.unmet.join('; ')}>build {rs.variant}{rs.unmet.length ? ' (unmet)' : ''}</button>
          {/each}
        </div>
      </div>
    {/each}
  </div>

  <div class="card overflow-auto">
    <h2 class="h2 mb-2">Installs</h2>
    <table class="table">
      <thead><tr><th>id</th><th>runtime</th><th>kind</th><th>version</th><th>path</th><th>facts</th><th></th></tr></thead>
      <tbody>
        {#each installs as i (i.id)}
          <tr>
            <td class="mono">{i.id}</td>
            <td>{i.runtimeId}</td>
            <td>{enumName(InstallKind, i.kind)}</td>
            <td>{i.version}</td>
            <td class="mono">{i.path}</td>
            <td class="muted text-xs">{Object.entries(i.facts).map(([k, v]) => `${k}=${v}`).join(' ').slice(0, 80)}</td>
            <td class="text-right"><button class="btn btn-danger" onclick={() => removeInstall(i.id)}>remove</button></td>
          </tr>
        {:else}
          <tr><td colspan="7" class="muted">no installs, adopt, install, or build one</td></tr>
        {/each}
      </tbody>
    </table>
  </div>

  <div class="card overflow-auto">
    <h2 class="h2 mb-2">Builds</h2>
    <table class="table">
      <thead><tr><th>id</th><th>recipe</th><th>variant</th><th>ref</th><th>state</th><th>sandbox</th><th>install</th><th>created</th><th></th></tr></thead>
      <tbody>
        {#each builds as b (b.id)}
          <tr>
            <td class="mono">{b.id}</td>
            <td>{b.recipeId}</td>
            <td>{b.variant}</td>
            <td class="mono">{b.ref}</td>
            <td><Badge state={enumName(BuildState, b.state)} />{#if b.error}<div class="text-xs text-red-300">{b.error}</div>{/if}</td>
            <td>{enumName(SandboxKind, b.sandbox)} {b.image}</td>
            <td class="mono">{b.installId}</td>
            <td class="muted">{when(b.createdAt)}</td>
            <td class="whitespace-nowrap text-right">
              {#if b.taskId}<button class="btn" onclick={() => (tasks = [b.taskId, ...tasks.filter((t) => t !== b.taskId)])}>log</button>{/if}
              <button class="btn btn-danger" onclick={() => removeBuild(b.id)}>remove</button>
            </td>
          </tr>
        {:else}
          <tr><td colspan="9" class="muted">no builds yet</td></tr>
        {/each}
      </tbody>
    </table>
  </div>

  {#each tasks as id (id)}
    <div class="card"><TaskLog {id} /></div>
  {/each}

  <Modal bind:open={adoptOpen} title="Adopt a binary for {adoptRuntime}">
    <label class="label" for="adopt-path">Executable path, searched on PATH when empty</label>
    <input id="adopt-path" class="input" bind:value={adoptPath} placeholder="/usr/local/bin/llama-server" />
    <div class="mt-4 flex justify-end"><button class="btn btn-primary" onclick={adopt}>adopt</button></div>
  </Modal>

  <Modal bind:open={buildOpen} title="Build {buildRecipe?.recipe?.id}">
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label class="label" for="build-variant">Variant</label>
        <select id="build-variant" class="input" bind:value={variant}>
          {#each buildRecipe?.recipe?.variants ?? [] as v (v.id)}
            <option value={v.id}>{v.id} {v.description ? '- ' + v.description : ''}</option>
          {/each}
        </select>
      </div>
      <div>
        <label class="label" for="build-sandbox">Sandbox</label>
        <select id="build-sandbox" class="input" bind:value={sandbox}>
          <option value="host">host toolchain</option>
          <option value="oci">container{buildRecipe?.sandboxCli ? ' via ' + buildRecipe.sandboxCli : ''}</option>
        </select>
      </div>
      {#if sandbox === 'oci'}
        <div class="col-span-2">
          <label class="label" for="build-image">Image</label>
          <input id="build-image" class="input" bind:value={image} placeholder="an image with the toolchain" />
        </div>
      {/if}
      <div>
        <label class="label" for="build-ref">Ref</label>
        <input id="build-ref" class="input" bind:value={ref} placeholder="recipe default, latest release" />
      </div>
      <div class="flex items-end gap-2 pb-1">
        <input id="build-force" type="checkbox" bind:checked={force} />
        <label for="build-force" class="text-sm">rebuild even when cached</label>
      </div>
      <div class="col-span-2">
        <label class="label" for="build-vars">Vars, one name=value per line</label>
        <textarea id="build-vars" class="input h-20" bind:value={vars} placeholder="build_type=Release"></textarea>
      </div>
    </div>
    {#if buildRecipe?.unmet.length}<div class="mt-2 text-xs text-amber-300">{buildRecipe.unmet.join('; ')}</div>{/if}
    <div class="mt-4 flex justify-end"><button class="btn btn-primary" onclick={build}>build</button></div>
  </Modal>
</div>
