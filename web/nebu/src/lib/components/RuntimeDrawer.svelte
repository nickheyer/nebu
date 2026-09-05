<script lang="ts">
  import { live, cached, clock, profilesOf } from '$lib/state.svelte';
  import { ago, enumLabel, newestFirst, when } from '$lib/format';
  import { ApiFlavor, InstallKind, ParamType } from '$proto/runtime_pb';
  import Drawer from './ui/Drawer.svelte';
  import Tabs from './ui/Tabs.svelte';
  import Badge from './ui/Badge.svelte';
  import Kv from './ui/Kv.svelte';
  import Section from './ui/Section.svelte';
  import ParamChips from './ui/ParamChips.svelte';

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

<Drawer bind:id title={manifest?.name || manifest?.id || 'Runtime'} subtitle={manifest?.id} width="lg">
  {#snippet header()}
    {#if status && manifest}
      <div class="flex flex-wrap items-center gap-2">
        <Badge tone={status.compatible ? 'ok' : 'warn'} dot label={status.compatible ? 'compatible' : 'incompatible'} />
        <span class="text-xs text-fg-muted">speaks {enumLabel(ApiFlavor, manifest.launch?.api)}</span>
        {#each manifest.formats as f (f)}<Badge size="xs" label={f} />{/each}
      </div>
      <div class="mt-3">
        <Tabs
          size="sm"
          bind:value={tab}
          tabs={[
            { id: 'overview', label: 'Overview' },
            { id: 'params', label: 'Params', count: manifest.params.length || undefined },
            { id: 'installs', label: 'Installs', count: installs.length || undefined },
            { id: 'profiles', label: 'Profiles', count: profiles.length || undefined }
          ]}
        />
      </div>
    {/if}
  {/snippet}

  {#if status && manifest}
    <div class="px-5 py-4">
      {#if tab === 'overview'}
        <div class="flex flex-col gap-5">
          {#if manifest.description}<p class="text-sm leading-6 text-fg-muted">{manifest.description}</p>{/if}
          {#if status.unmet.length}
            <Section title="Unmet requirements">
              <ul class="rounded-md border border-warn/30 bg-warn/8 px-3 py-2 text-xs leading-5 text-warn">
                {#each status.unmet as u (u)}<li>{u}</li>{/each}
              </ul>
            </Section>
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
                ['prebuilt', manifest.acquire?.prebuilt.length ? `${manifest.acquire.prebuilt.length} release rules` : 'none'],
                ['recipe', manifest.acquire?.recipeId],
                ['stop grace', manifest.launch?.stopGraceMs ? `${manifest.launch.stopGraceMs / 1000}s` : undefined]
              ]}
            />
          </Section>
        </div>
      {:else if tab === 'params'}
        {#if manifest.params.length === 0}
          <p class="text-sm text-fg-faint">This runtime declares no params.</p>
        {:else}
          <table class="tbl">
            <thead><tr><th>name</th><th>type</th><th>default</th><th>choices</th><th>description</th></tr></thead>
            <tbody>
              {#each manifest.params as p (p.name)}
                <tr>
                  <td class="font-mono text-xs text-fg whitespace-nowrap">{p.name}{#if p.solved}<span class="ml-1.5 font-sans text-[10.5px] text-fg-faint" title="The planner solves it when set to auto">auto</span>{/if}</td>
                  <td class="text-xs text-fg-muted">{enumLabel(ParamType, p.type)}</td>
                  <td class="font-mono text-xs text-fg-muted">{p.default || '–'}</td>
                  <td class="max-w-[12rem] truncate font-mono text-xs text-fg-muted" title={p.choices.join(', ')}>{p.choices.join(', ') || '–'}</td>
                  <td class="max-w-md text-xs leading-5 text-fg-muted">{p.description}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {:else if tab === 'installs'}
        {#if installs.length === 0}
          <p class="text-sm text-fg-faint">No install of this runtime yet. Adopt a binary, download a prebuilt release, or build one.</p>
        {:else}
          <table class="tbl">
            <thead><tr><th>kind</th><th>version</th><th>path</th><th>facts</th><th>added</th></tr></thead>
            <tbody>
              {#each installs as i (i.id)}
                <tr>
                  <td><Badge size="xs" label={enumLabel(InstallKind, i.kind)} tone={i.kind === InstallKind.BUILT ? 'accent' : 'neutral'} /></td>
                  <td class="font-mono text-xs">{i.version || '–'}</td>
                  <td class="max-w-xs truncate font-mono text-xs text-fg-muted" title={i.path}>{i.path}</td>
                  <td class="max-w-sm"><ParamChips params={i.facts} max={4} /></td>
                  <td class="text-xs text-fg-muted" title={when(i.createdAt)}>{ago(i.createdAt, clock.now)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {:else if tab === 'profiles'}
        {#if profiles.length === 0}
          <p class="text-sm text-fg-faint">No profiles for this runtime. Runs start from the manifest defaults.</p>
        {:else}
          <table class="tbl">
            <thead><tr><th>name</th><th>params</th><th>updated</th></tr></thead>
            <tbody>
              {#each profiles as p (p.id)}
                <tr>
                  <td>
                    <div class="flex items-center gap-2">
                      <span class="font-mono text-xs text-fg">{p.name}</span>
                      {#if p.default}<Badge tone="accent" size="xs" label="default" />{/if}
                    </div>
                    {#if p.description}<div class="text-[11px] text-fg-faint">{p.description}</div>{/if}
                  </td>
                  <td class="max-w-md">
                    {#if Object.keys(p.params).length}<ParamChips params={p.params} />{:else}<span class="text-[11px] text-fg-faint">manifest defaults</span>{/if}
                  </td>
                  <td class="text-xs text-fg-muted" title={when(p.updatedAt)}>{ago(p.updatedAt, clock.now)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      {/if}
    </div>
  {/if}
</Drawer>
