<script lang="ts">
  import { api } from '$lib/api';
  import { live, clock } from '$lib/state.svelte';
  import { sandboxWord } from '$lib/runtimes';
  import { ago, duration, when } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { BuildState, type Build } from '$proto/recipe_pb';
  import { ScrollText, Trash2 } from '@lucide/svelte';
  import State from '../ui/State.svelte';
  import IconButton from '../ui/IconButton.svelte';
  import ArmedButton from '../ui/ArmedButton.svelte';

  let { builds }: { builds: Build[] } = $props();

  let removing = $state('');

  async function remove(b: Build) {
    removing = b.id;
    try {
      await api.builds.removeBuild({ id: b.id });
      live.builds.delete(b.id);
      ok(`Removed build ${b.variant || b.id}`);
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
      <th>Variant</th>
      <th>Ref</th>
      <th>Sandbox</th>
      <th>State</th>
      <th class="num">Took</th>
      <th>Started</th>
      <th></th>
    </tr>
  </thead>
  <tbody>
    {#each builds as b (b.id)}
      {@const running = b.state === BuildState.RUNNING}
      <tr>
        <td>
          <span class="font-mono text-fg">{b.variant || '–'}</span>
          {#if b.error}<div class="max-w-md truncate text-xs text-bad" title={b.error}>{b.error}</div>{/if}
        </td>
        <td class="font-mono text-xs text-fg-muted">{b.ref || '–'}{#if b.commit}<span class="text-fg-faint">{' '}{b.commit.slice(0, 7)}</span>{/if}</td>
        <td class="text-fg-muted">{sandboxWord(b.sandbox) || '–'}{#if b.image}<span class="font-mono text-xs">{' '}{b.image}</span>{/if}</td>
        <td><State values={BuildState} value={b.state} /></td>
        <td class="num text-fg-muted">{duration(b.createdAt, b.finishedAt, clock.now)}</td>
        <td class="whitespace-nowrap text-fg-muted" title={when(b.createdAt)}>{ago(b.createdAt, clock.now)}</td>
        <td class="actions">
          <span>
            {#if b.taskId}<IconButton size="sm" icon={ScrollText} label="Open task" href="/tasks/{b.taskId}" />{/if}
            {#if !running}<ArmedButton compact icon={Trash2} label="Remove" armed="Remove build" loading={removing === b.id} onconfirm={() => remove(b)} />{/if}
          </span>
        </td>
      </tr>
    {/each}
  </tbody>
</table>
