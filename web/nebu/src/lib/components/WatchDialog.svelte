<script lang="ts">
  import { api } from '$lib/api';
  import { cached } from '$lib/state.svelte';
  import { createForm } from '$lib/form.svelte';
  import FormDialog from './ui/FormDialog.svelte';
  import Field from './ui/Field.svelte';
  import Checkbox from './ui/Checkbox.svelte';
  import RunTarget from './RunTarget.svelte';

  let { open = $bindable(false), sourceId = '', repo = '' }: { open?: boolean; sourceId?: string; repo?: string } = $props();

  let source = $state('');
  let repoText = $state('');
  let revision = $state('');
  let match = $state('');
  let autoPull = $state(false);
  let slotId = $state('');
  let runtimeId = $state('');
  let profileId = $state('');
  let values = $state<Record<string, string>>({});
  let invalid = $state(0);

  const sources = $derived(cached.sources.filter((s) => s.source));
  // The repository form the chosen source takes, as its provider states it
  const repoExample = $derived(sources.find((s) => s.source?.id === source)?.capabilities?.repoExample || 'owner/name');

  const form = createForm({
    open: () => open,
    close: () => (open = false),
    reset() {
      source = sourceId || sources[0]?.source?.id || '';
      repoText = repo;
      revision = match = slotId = runtimeId = profileId = '';
      values = {};
      autoPull = false;
    },
    async submit() {
      await api.monitor.addWatch({ sourceId: source, repo: repoText.trim(), revision, groupMatch: match, autoPull: autoPull || !!slotId, slotId, runtimeId, params: values, profileId });
      return { title: `Watching ${repoText.trim()}`, link: { href: '/monitor', label: 'Monitor' } };
    },
    failTitle: 'Watch refused'
  });
</script>

<FormDialog bind:open title="Watch a repository" description="Checked on an interval for new commits and weight groups" size="lg" action="Watch" saving={form.saving} disabled={!repoText.trim() || !source || invalid > 0} onsubmit={form.run}>
  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
    <Field label="Source" for="w-source">
      {#if sources.length <= 1}
        <div id="w-source" class="input-static">{sources[0]?.source?.name || sources[0]?.source?.id || '–'}</div>
      {:else}
        <select id="w-source" class="input" bind:value={source}>
          {#each sources as s (s.source?.id)}<option value={s.source?.id}>{s.source?.name || s.source?.id}</option>{/each}
        </select>
      {/if}
    </Field>
    <Field label="Repository" for="w-repo">
      <input id="w-repo" class="input font-mono" bind:value={repoText} placeholder={repoExample} autocomplete="off" spellcheck="false" />
    </Field>
    <Field label="Revision" for="w-rev" hint="Empty follows the default branch or latest release">
      <input id="w-rev" class="input font-mono" bind:value={revision} placeholder="default" autocomplete="off" spellcheck="false" />
    </Field>
    <Field label="Group match" for="w-match" hint="A pattern the weight group has to match">
      <input id="w-match" class="input font-mono" bind:value={match} placeholder="Q4_K_M|Q5_K_M" autocomplete="off" spellcheck="false" />
    </Field>
    <Checkbox bind:checked={autoPull} class="sm:col-span-2" title="Pull on change" hint="New matching weights are pulled into the library as they appear" />
    <RunTarget optional idPrefix="w" bind:slotId bind:runtimeId bind:profileId bind:values bind:invalid />
  </div>
</FormDialog>
