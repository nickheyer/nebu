<script lang="ts">
  import { api } from '$lib/api';
  import { live, cached } from '$lib/state.svelte';
  import { createForm } from '$lib/form.svelte';
  import { groupByProvider } from '$lib/catalog';
  import { SourceKind } from '$proto/source_pb';
  import FormDialog from './ui/FormDialog.svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Checkbox from './ui/Checkbox.svelte';
  import Section from './ui/Section.svelte';
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
  const whereItems = $derived(
    groups.flatMap((g) => [
      { value: `kind:${g.kind}`, label: g.name, detail: g.sources.length > 1 ? `all ${g.sources.length}` : undefined },
      ...(g.sources.length > 1 ? g.sources.map((s) => ({ value: `source:${s.source?.id}`, label: s.source?.name || s.source?.id || '', group: g.name })) : [])
    ])
  );

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

<FormDialog bind:open title="Want a model" size="lg" action="Want" saving={form.saving} disabled={!text.trim() || invalid > 0} onsubmit={form.run}>
  <div class="flex flex-col gap-6">
    <div class="grid grid-cols-1 gap-x-4 gap-y-5 sm:grid-cols-2">
      <Field label="Query" for="want-query" class="sm:col-span-2" info="Searched on every check until a weight group matches">
        <input id="want-query" class="input" bind:value={text} placeholder="model name" autocomplete="off" spellcheck="false" />
      </Field>
      <Field label="Where" for="want-where">
        <Select id="want-where" bind:value={where} items={[{ value: '', label: 'All sources' }, ...whereItems]} />
      </Field>
      <Field label="Format" for="want-format">
        <Select id="want-format" bind:value={formatId} items={[{ value: '', label: 'Any' }, ...[...live.formats.values()].map((f) => ({ value: f.id, label: f.description || f.id }))]} />
      </Field>
      <Field label="Group match" for="want-match" class="sm:col-span-2" info="A pattern the weight group has to match">
        <input id="want-match" class="input font-mono" bind:value={match} placeholder="Q4_K_M|Q5_K_M" autocomplete="off" spellcheck="false" />
      </Field>
    </div>
    <Section title="When found">
      <div class="grid grid-cols-1 gap-x-4 gap-y-5 sm:grid-cols-2">
        <Checkbox bind:checked={autoPull} class="sm:col-span-2" label="Pull the first match" />
        <RunTarget optional idPrefix="want" bind:slotId bind:runtimeId bind:profileId bind:values bind:invalid />
      </div>
    </Section>
  </div>
</FormDialog>
