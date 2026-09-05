<script lang="ts">
  import { api } from '$lib/api';
  import { live, cached } from '$lib/state.svelte';
  import { createForm } from '$lib/form.svelte';
  import { groupByProvider } from '$lib/catalog';
  import { SourceKind } from '$proto/source_pb';
  import FormDialog from './ui/FormDialog.svelte';
  import Field from './ui/Field.svelte';
  import Checkbox from './ui/Checkbox.svelte';
  import RunTarget from './RunTarget.svelte';

  let { open = $bindable(false), query = '' }: { open?: boolean; query?: string } = $props();

  let text = $state('');
  // Where to look: everything, one provider as kind:N, or one source as source:ID
  let where = $state('');
  let match = $state('');
  let formatId = $state('');
  let autoPull = $state(false);
  let slotId = $state('');
  let runtimeId = $state('');
  let profileId = $state('');
  let values = $state<Record<string, string>>({});
  let invalid = $state(0);

  const groups = $derived(groupByProvider(cached.sources));

  const form = createForm({
    open: () => open,
    close: () => (open = false),
    reset() {
      text = query;
      where = match = formatId = slotId = runtimeId = profileId = '';
      values = {};
      autoPull = false;
    },
    async submit() {
      const [scope, id] = where.split(':');
      await api.monitor.addWant({
        query: text.trim(),
        kind: scope === 'kind' ? (Number(id) as SourceKind) : SourceKind.UNSPECIFIED,
        sourceId: scope === 'source' ? id : '',
        groupMatch: match,
        formatId,
        autoPull: autoPull || !!slotId,
        slotId,
        runtimeId,
        params: values,
        profileId
      });
      return { title: `Wanting ${text.trim()}` };
    },
    failTitle: 'Want refused'
  });
</script>

<FormDialog bind:open title="Want a model" description="A search run on every check until a weight group matches" size="lg" action="Want" saving={form.saving} disabled={!text.trim() || invalid > 0} onsubmit={form.run}>
  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
    <Field label="Query" for="want-query" class="sm:col-span-2">
      <input id="want-query" class="input" bind:value={text} placeholder="model name" autocomplete="off" spellcheck="false" />
    </Field>
    <Field label="Where" for="want-where">
      <select id="want-where" class="input" bind:value={where}>
        <option value="">All sources</option>
        {#each groups as g (g.kind)}
          <option value="kind:{g.kind}">{g.name}{g.sources.length > 1 ? ` · all ${g.sources.length}` : ''}</option>
          {#if g.sources.length > 1}
            {#each g.sources as s (s.source?.id)}<option value="source:{s.source?.id}">&nbsp;&nbsp;{s.source?.name || s.source?.id}</option>{/each}
          {/if}
        {/each}
      </select>
    </Field>
    <Field label="Format" for="want-format">
      <select id="want-format" class="input" bind:value={formatId}>
        <option value="">Any</option>
        {#each [...live.formats.values()] as f (f.id)}<option value={f.id}>{f.description || f.id}</option>{/each}
      </select>
    </Field>
    <Field label="Group match" for="want-match" class="sm:col-span-2" hint="A pattern the weight group has to match">
      <input id="want-match" class="input font-mono" bind:value={match} placeholder="Q4_K_M|Q5_K_M" autocomplete="off" spellcheck="false" />
    </Field>
    <Checkbox bind:checked={autoPull} class="sm:col-span-2" title="Pull when found" hint="The first match is pulled into the library" />
    <RunTarget optional idPrefix="want" bind:slotId bind:runtimeId bind:profileId bind:values bind:invalid />
  </div>
</FormDialog>
