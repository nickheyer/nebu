<script lang="ts">
  import { live, cached, clock, profilesOf } from '$lib/state.svelte';
  import { ago, enumLabel, middle, newestFirst, when } from '$lib/format';
  import { ApiFlavor, InstallKind, ParamType } from '$proto/runtime_pb';
  import Drawer from './ui/Drawer.svelte';
  import Tabs from './ui/Tabs.svelte';
  import Kv from './ui/Kv.svelte';
  import State from './ui/State.svelte';
  import Section from './ui/Section.svelte';
  import ParamList from './ui/ParamList.svelte';

  let { id = $bindable('') }: { id?: string } = $props();

  let tab = $state('overview');
  const status = $derived(id ? cached.runtimes.find((r) => r.manifest?.id === id) : undefined);
  const manifest = $derived(status?.manifest);
  const installs = $derived([...live.installs.values()].filter((i) => i.runtimeId === id).sort(newestFirst((i) => i.createdAt)));
  const profiles = $derived(profilesOf(id));

  $effect(() => {
    if (id) tab = 'overview';
  });
</script>

<Drawer bind:id title={manifest?.name || manifest?.id || 'Runtime'} subtitle={manifest?.description || manifest?.id}>
  {#snippet header()}
    {#if status && manifest}
      <div class="flex flex-wrap items-center gap-3 text-sm text-fg-muted">
        <State tone={status.compatible ? 'ok' : 'warn'} label={status.compatible ? 'Compatible' : 'Incompatible'} />
        <span>{enumLabel(ApiFlavor, manifest.launch?.api)} API</span>
        <span class="font-mono text-xs">{manifest.formats.join(' ')}</span>
      </div>
      <Tabs
        size="sm"
        class="mt-3"
        bind:value={tab}
        tabs={[
          { id: 'overview', label: 'Overview' },
          { id: 'params', label: 'Parameters', count: manifest.params.length || undefined },
          { id: 'installs', label: 'Installs', count: installs.length || undefined },
          { id: 'profiles', label: 'Profiles', count: profiles.length || undefined }
        ]}
      />
    {/if}
  {/snippet}

  {#if status && manifest}
    <div class="px-6 py-5">
      {#if tab === 'overview'}
        <div class="flex flex-col gap-6">
          {#if status.unmet.length}
            <ul class="note note-warn list-disc pl-6">
              {#each status.unmet as u (u)}<li>{u}</li>{/each}
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
                ['adopts', manifest.acquire?.adopt.join(', ')],
                ['prebuilt', manifest.acquire?.prebuilt.length ? `${manifest.acquire.prebuilt.length} release rules` : '–'],
                ['recipe', manifest.acquire?.recipeId],
                ['stop grace', manifest.launch?.stopGraceMs ? `${manifest.launch.stopGraceMs / 1000}s` : undefined]
              ]}
            />
          </Section>
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
                  <td class="font-mono text-xs whitespace-nowrap text-fg">{p.name}{#if p.solved}<span class="ml-1.5 font-sans text-xs text-fg-faint" title="Solved by the planner when auto">auto</span>{/if}</td>
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
          <p class="text-sm text-fg-faint">Not installed</p>
        {:else}
          <table class="tbl">
            <thead><tr><th>Kind</th><th>Version</th><th>Path</th><th>Facts</th><th>Added</th></tr></thead>
            <tbody>
              {#each installs as i (i.id)}
                <tr>
                  <td class="text-fg-muted">{enumLabel(InstallKind, i.kind)}</td>
                  <td class="font-mono text-xs">{i.version || '–'}</td>
                  <td class="font-mono text-xs text-fg-muted" title={i.path}>{middle(i.path, 36)}</td>
                  <td class="max-w-sm"><ParamList params={i.facts} max={4} /></td>
                  <td class="text-fg-muted" title={when(i.createdAt)}>{ago(i.createdAt, clock.now)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {:else if tab === 'profiles'}
        {#if profiles.length === 0}
          <p class="text-sm text-fg-faint">No profiles</p>
        {:else}
          <table class="tbl">
            <thead><tr><th>Name</th><th>Parameters</th><th>Updated</th></tr></thead>
            <tbody>
              {#each profiles as p (p.id)}
                <tr>
                  <td>
                    <span class="font-mono text-xs text-fg">{p.name}</span>
                    {#if p.default}<span class="ml-1.5 text-xs text-fg-faint">default</span>{/if}
                    {#if p.description}<div class="text-xs text-fg-faint">{p.description}</div>{/if}
                  </td>
                  <td class="max-w-md">
                    {#if Object.keys(p.params).length}<ParamList params={p.params} />{:else}<span class="text-xs text-fg-faint">runtime defaults</span>{/if}
                  </td>
                  <td class="text-fg-muted" title={when(p.updatedAt)}>{ago(p.updatedAt, clock.now)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {/if}
    </div>
  {/if}
</Drawer>
