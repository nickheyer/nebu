<script lang="ts">
  import { api } from '$lib/api';
  import { cached } from '$lib/state.svelte';
  import { createForm } from '$lib/form.svelte';
  import FormDialog from './ui/FormDialog.svelte';
  import Field from './ui/Field.svelte';
  import CheckCard from './ui/CheckCard.svelte';
  import SwapTarget from './SwapTarget.svelte';

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
      return { title: `Watching ${repoText.trim()}` };
    },
    failTitle: 'Watch refused'
  });
</script>

<FormDialog
  bind:open
  title="Watch a repository"
  action="Watch"
  saving={form.saving}
  disabled={!repoText.trim() || !source || invalid > 0}
  onsubmit={form.run}
>
  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
    <Field label="Source" for="w-source">
      <select id="w-source" class="input" bind:value={source}>
        {#each sources as s (s.source?.id)}<option value={s.source?.id}>{s.source?.name || s.source?.id}</option>{/each}
      </select>
    </Field>
    <Field label="Repository" for="w-repo">
      <input id="w-repo" class="input font-mono" bind:value={repoText} placeholder={repoExample} />
    </Field>
    <Field label="Revision" for="w-rev">
      <input id="w-rev" class="input font-mono" bind:value={revision} placeholder="main" />
    </Field>
    <Field label="Group match" for="w-match" hint="Regex over weight group names">
      <input id="w-match" class="input font-mono" bind:value={match} placeholder="regex" />
    </Field>
    <CheckCard bind:checked={autoPull} class="sm:col-span-2" title="Pull on change" />
    <SwapTarget optional idPrefix="w" bind:slotId bind:runtimeId bind:profileId bind:values bind:invalid />
  </div>
</FormDialog>
