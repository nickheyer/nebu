<script lang="ts">
  import { api } from '$lib/api';
  import { createForm } from '$lib/form.svelte';
  import { parsePairs, pairsText } from '$lib/format';
  import { SandboxKind, type RecipeStatus } from '$proto/recipe_pb';
  import { Hammer, ChevronDown } from '@lucide/svelte';
  import FormDialog from './ui/FormDialog.svelte';
  import Field from './ui/Field.svelte';
  import Checkbox from './ui/Checkbox.svelte';

  let { open = $bindable(false), recipe }: { open?: boolean; recipe: RecipeStatus | null } = $props();

  let variant = $state('');
  let sandbox = $state<'host' | 'oci'>('host');
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

<FormDialog bind:open title="Build {recipe?.recipe?.runtimeId ?? ''}" description={recipe?.recipe?.description} size="lg" action="Build" icon={Hammer} saving={form.saving} disabled={!variant} onsubmit={form.run}>
  {#if recipe?.recipe}
    <div class="flex flex-col gap-4">
      {#if recipe.missingTools.length || recipe.unmet.length}
        <ul class="note note-warn list-disc pl-6">
          {#if recipe.missingTools.length}<li>Missing tools: {recipe.missingTools.join(' ')}</li>{/if}
          {#each recipe.unmet as u (u)}<li>{u}</li>{/each}
        </ul>
      {/if}
      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <Field label="Variant" for="b-variant" hint={selected?.description}>
          <select id="b-variant" class="input" bind:value={variant}>
            {#each recipe.recipe.variants as v (v.id)}
              <option value={v.id}>{v.id}{v.id === recipe.variant ? ' · fits this host' : ''}</option>
            {/each}
          </select>
        </Field>
        <Field label="Sandbox" for="b-sandbox">
          <select id="b-sandbox" class="input" bind:value={sandbox}>
            <option value="host">Host toolchain</option>
            <option value="oci" disabled={!recipe.sandboxCli}>{recipe.sandboxCli ? `Container · ${recipe.sandboxCli}` : 'Container · no CLI on PATH'}</option>
          </select>
        </Field>
        {#if sandbox === 'oci'}
          <Field label="Image" for="b-image" class="sm:col-span-2">
            <input id="b-image" class="input font-mono" bind:value={image} placeholder={selected?.image || recipe.recipe.sandbox?.image || 'image with the toolchain'} autocomplete="off" spellcheck="false" />
          </Field>
        {/if}
        <Field label="Ref" for="b-ref" hint="A tag, branch, or commit">
          <input id="b-ref" class="input font-mono" bind:value={ref} placeholder={recipe.recipe.source?.ref || 'latest release'} autocomplete="off" spellcheck="false" />
        </Field>
        <Checkbox bind:checked={force} class="self-end pb-2" title="Rebuild even if cached" />
        <Field label="Variables" for="b-vars" class="sm:col-span-2" hint="One name=value per line">
          <textarea id="b-vars" class="input h-20 font-mono text-xs" bind:value={varsText} placeholder={pairsText(recipe.vars).split('\n').slice(0, 2).join('\n') || 'name=value'}></textarea>
        </Field>
      </div>
      {#if recipe.recipe.steps.length}
        <div class="rounded-lg border border-line">
          <button type="button" class="flex w-full items-center gap-2 px-3 py-2 text-left text-sm text-fg-muted hover:text-fg" aria-expanded={showSteps} onclick={() => (showSteps = !showSteps)}>
            <ChevronDown size={14} class="transition-transform {showSteps ? 'rotate-180' : ''}" />
            {recipe.recipe.steps.length} build steps
          </button>
          {#if showSteps}
            <ol class="border-t border-line px-3 py-2 text-sm">
              {#each recipe.recipe.steps as s, i (i)}
                <li class="flex gap-3 py-1"><span class="w-5 tabular-nums text-fg-faint">{i + 1}</span><span class="text-fg">{s.name}</span><span class="ml-auto truncate font-mono text-xs text-fg-faint" title={s.command.join(' ')}>{s.command[0]}</span></li>
              {/each}
            </ol>
          {/if}
        </div>
      {/if}
    </div>
  {/if}
</FormDialog>
