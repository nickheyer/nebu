<script lang="ts">
  import { api } from '$lib/api';
  import { createForm } from '$lib/form.svelte';
  import { parsePairs, pairsText } from '$lib/format';
  import { SandboxKind, type RecipeStatus } from '$proto/recipe_pb';
  import { Hammer } from '@lucide/svelte';
  import FormDialog from './ui/FormDialog.svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Checkbox from './ui/Checkbox.svelte';
  import Disclosure from './ui/Disclosure.svelte';

  let { open = $bindable(false), recipe }: { open?: boolean; recipe: RecipeStatus | null } = $props();

  let variant = $state('');
  let sandbox = $state('host');
  let image = $state('');
  let ref = $state('');
  let varsText = $state('');
  let force = $state(false);
  let showSteps = $state(false);

  const selected = $derived(recipe?.recipe?.variants.find((v) => v.id === variant));

  const form = createForm({
    open: () => open,
    close: () => (open = false),
    reset() {
      variant = recipe?.variant || recipe?.recipe?.variants[0]?.id || '';
      sandbox = recipe?.sandbox === SandboxKind.OCI ? 'oci' : 'host';
      image = ref = varsText = '';
      force = false;
      showSteps = false;
    },
    async submit() {
      if (!recipe?.recipe) return;
      const r = await api.builds.build({
        recipeId: recipe.recipe.id,
        runtimeId: recipe.recipe.runtimeId,
        variant,
        sandbox: sandbox === 'oci' ? SandboxKind.OCI : SandboxKind.HOST,
        image,
        ref,
        vars: parsePairs(varsText),
        force
      });
      const hit = r.task?.labels['cached'] === 'true';
      return {
        title: hit ? 'Build already cached' : `Building ${recipe.recipe.runtimeId}`,
        detail: hit ? 'Reusing it' : variant,
        link: r.task ? { href: `/tasks?id=${r.task.id}`, label: 'Task' } : undefined
      };
    },
    failTitle: 'Build refused'
  });
</script>

<FormDialog bind:open title="Build {recipe?.recipe?.runtimeId ?? ''}" subtitle={recipe?.recipe?.description} size="lg" action="Build" icon={Hammer} saving={form.saving} disabled={!variant} onsubmit={form.run}>
  {#if recipe?.recipe}
    <div class="flex flex-col gap-5">
      {#if recipe.missingTools.length || recipe.unmet.length}
        <ul class="note note-warn list-disc pl-6">
          {#if recipe.missingTools.length}<li>Missing tools: {recipe.missingTools.join(' ')}</li>{/if}
          {#each recipe.unmet as u (u)}<li>{u}</li>{/each}
        </ul>
      {/if}
      <div class="grid grid-cols-1 gap-x-4 gap-y-5 sm:grid-cols-2">
        <Field label="Variant" for="b-variant" info={selected?.description || undefined}>
          <Select id="b-variant" bind:value={variant} items={recipe.recipe.variants.map((v) => ({ value: v.id, label: v.id, detail: v.id === recipe.variant ? 'fits this host' : undefined }))} />
        </Field>
        <Field label="Sandbox" for="b-sandbox">
          <Select
            id="b-sandbox"
            bind:value={sandbox}
            items={[
              { value: 'host', label: 'Host toolchain' },
              { value: 'oci', label: 'Container', detail: recipe.sandboxCli || 'no CLI on PATH', disabled: !recipe.sandboxCli }
            ]}
          />
        </Field>
        {#if sandbox === 'oci'}
          <Field label="Image" for="b-image" class="sm:col-span-2">
            <input id="b-image" class="input font-mono" bind:value={image} placeholder={selected?.image || recipe.recipe.sandbox?.image || 'image with the toolchain'} autocomplete="off" spellcheck="false" />
          </Field>
        {/if}
        <Field label="Ref" for="b-ref" info="A tag, branch, or commit">
          <input id="b-ref" class="input font-mono" bind:value={ref} placeholder={recipe.recipe.source?.ref || 'latest release'} autocomplete="off" spellcheck="false" />
        </Field>
        <Checkbox bind:checked={force} class="self-end" label="Rebuild even if cached" />
        <Field label="Variables" for="b-vars" class="sm:col-span-2" info="One name=value per line">
          <textarea id="b-vars" class="input h-20 font-mono text-xs" bind:value={varsText} placeholder={pairsText(recipe.vars).split('\n').slice(0, 2).join('\n') || 'name=value'}></textarea>
        </Field>
      </div>
      {#if recipe.recipe.steps.length}
        <Disclosure label="Steps" summary={String(recipe.recipe.steps.length)} bind:open={showSteps}>
          <ol class="text-sm">
            {#each recipe.recipe.steps as s, i (i)}
              <li class="flex gap-3 py-1"><span class="w-5 tabular-nums text-fg-faint">{i + 1}</span><span class="text-fg">{s.name}</span><span class="ml-auto truncate font-mono text-xs text-fg-faint" title={s.command.join(' ')}>{s.command[0]}</span></li>
            {/each}
          </ol>
        </Disclosure>
      {/if}
    </div>
  {/if}
</FormDialog>
