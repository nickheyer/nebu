<script lang="ts">
  import { api } from '$lib/api';
  import { live, clock, homePath, instanceLive } from '$lib/state.svelte';
  import { kindWord, sandboxWord } from '$lib/runtimes';
  import { ago, tail, when } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import type { Install } from '$proto/runtime_pb';
  import { ChevronRight, Trash2 } from '@lucide/svelte';
  import State from '../ui/State.svelte';
  import Kv from '../ui/Kv.svelte';
  import Copy from '../ui/Copy.svelte';
  import ArmedButton from '../ui/ArmedButton.svelte';

  // Every usable copy of one runtime, newest first, each row opening to everything recorded about it
  let { installs }: { installs: Install[] } = $props();

  let open = $state('');
  let removing = $state('');

  // The names the instances running on an install serve as
  function serving(i: Install): string[] {
    return [...live.instances.values()].filter((x) => x.installId === i.id && instanceLive(x)).map((x) => (x.slotId ? (live.slots.get(x.slotId)?.name ?? x.name) : x.name));
  }

  const dirname = (path: string) => path.slice(0, path.lastIndexOf('/')) || '/';
  const capital = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

  // What the row does not show: the id, where it lives, where it came from, what its probes read, and
  // the build that made it, each fact once and none that the row, the path, or the origin already states
  function details(i: Install): [string, string][] {
    const b = i.buildId ? live.builds.get(i.buildId) : undefined;
    const said = new Set([i.version, b?.ref, b?.commit, ...i.origin.split(/\s+/), ...i.path.split(/[\\/]/)]);
    const rows = new Map<string, [string, string]>();
    const put = (label: string, value: string) => {
      if (value && !said.has(value) && !rows.has(label.toLowerCase())) rows.set(label.toLowerCase(), [label, value]);
    };
    put('Id', i.id);
    if (i.dir !== dirname(i.path)) put('Directory', homePath(i.dir));
    if (i.origin !== i.path) put('Origin', i.origin);
    for (const [k, v] of Object.entries(i.facts).sort(([a], [c]) => a.localeCompare(c))) put(capital(k), v);
    if (b) {
      put('Sandbox', b.image ? `${sandboxWord(b.sandbox)} · ${b.image}` : sandboxWord(b.sandbox));
      for (const [k, v] of Object.entries(b.vars).sort(([a], [c]) => a.localeCompare(c))) put(`var ${k}`, v);
      put('Patches', b.patches.join(', '));
    }
    return [...rows.values()];
  }

  async function remove(i: Install) {
    removing = i.id;
    try {
      await api.runtimes.removeInstall({ id: i.id });
      live.installs.delete(i.id);
      if (open === i.id) open = '';
      ok(`Removed ${i.version || tail(i.path)}`);
    } catch (err) {
      fail(err, 'Remove failed');
    } finally {
      removing = '';
    }
  }
</script>

<table class="tbl">
  <thead>
    <tr>
      <th class="w-6"></th>
      <th>Version</th>
      <th>Path</th>
      <th>Added</th>
      <th></th>
    </tr>
  </thead>
  <tbody>
    {#each installs as i (i.id)}
      {@const on = open === i.id}
      {@const names = serving(i)}
      {@const build = i.buildId ? live.builds.get(i.buildId) : undefined}
      <tr class="row-link {on ? 'row-active' : ''}" onclick={() => (open = on ? '' : i.id)}>
        <td class="w-6 pr-0!"><ChevronRight size={14} class="text-fg-faint transition-transform {on ? 'rotate-90' : ''}" /></td>
        <td>
          <div class="flex items-center gap-3">
            <span class="font-mono text-fg">{i.version || '–'}</span>
            <span class="text-xs text-fg-faint">{kindWord(i.kind)}</span>
            {#each names as n (n)}<State tone="ok" label="Serving as {n}" />{/each}
          </div>
          {#if build?.variant}<div class="font-mono text-xs text-fg-muted">{build.variant}</div>{/if}
        </td>
        <td class="max-w-md truncate font-mono text-xs text-fg-muted" title={i.path}>{homePath(i.path)}</td>
        <td class="whitespace-nowrap text-fg-muted" title={when(i.createdAt)}>{ago(i.createdAt, clock.now)}</td>
        <td class="actions" onclick={(e) => e.stopPropagation()}>
          <span><ArmedButton compact icon={Trash2} label="Remove" armed="Remove {i.version || 'install'}" loading={removing === i.id} disabled={names.length > 0} onconfirm={() => remove(i)} /></span>
        </td>
      </tr>
      {#if on}
        <tr class="row-active">
          <td></td>
          <td colspan="4" class="pb-4">
            <div class="flex items-start gap-2">
              <Kv mono items={details(i)} class="flex-1" />
              <Copy text={i.path} label="Copy path" />
            </div>
          </td>
        </tr>
      {/if}
    {/each}
  </tbody>
</table>
