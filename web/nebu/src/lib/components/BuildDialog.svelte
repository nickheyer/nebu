<script lang="ts">
  import { api } from '$lib/api';
  import { createForm } from '$lib/form.svelte';
  import { parsePairs, pairsText } from '$lib/format';
  import { SandboxKind, type RecipeStatus } from '$proto/recipe_pb';
  import { Hammer } from '@lucide/svelte';
  import FormDialog from './ui/FormDialog.svelte';
  import Field from './ui/Field.svelte';
  import CheckCard from './ui/CheckCard.svelte';

  let { open = $bindable(false), recipe }: { open?: boolean; recipe: RecipeStatus | null } = $props();

  let variant = $state('');
  let sandbox = $state<'host' | 'oci'>('host');
  let image = $state('');
  let ref = $state('');
  let varsText = $state('');
  let force = $state(false);

  const selected = $derived(recipe?.recipe?.variants.find((v) => v.id === variant));

  const form = createForm({
    open: () => open,
    close: () => (open = false),
    reset() {
      variant = recipe?.variant || recipe?.recipe?.variants[0]?.id || '';
      sandbox = recipe?.sandbox === SandboxKind.OCI ? 'oci' : 'host';
      image = ref = varsText = '';
      force = false;
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
        detail: hit ? 'An identical build exists, reusing it' : `${variant} variant`,
        link: r.task ? { href: `/tasks?id=${r.task.id}`, label: 'Follow the build' } : undefined
      };
    },
    failTitle: 'Build refused'
  });
</script>

<FormDialog bind:open title="Build {recipe?.recipe?.runtimeId ?? ''}" description={recipe?.recipe?.description} size="lg" action="Start build" icon={Hammer} saving={form.saving} disabled={!variant} onsubmit={form.run}>
  {#if recipe?.recipe}
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <Field label="Variant" for="b-variant" hint={selected?.description}>
        <select id="b-variant" class="input" bind:value={variant}>
          {#each recipe.recipe.variants as v (v.id)}
            <option value={v.id}>{v.id}{v.id === recipe.variant ? ' · host selected' : ''}</option>
          {/each}
        </select>
      </Field>
      <Field label="Sandbox" for="b-sandbox" hint={sandbox === 'oci' ? (recipe.sandboxCli ? `Runs through ${recipe.sandboxCli}` : 'No container CLI was found on PATH') : 'Runs with the host toolchain'}>
        <select id="b-sandbox" class="input" bind:value={sandbox}>
          <option value="host">Host toolchain</option>
          <option value="oci">Container</option>
        </select>
      </Field>
      {#if sandbox === 'oci'}
        <Field label="Image" for="b-image" hint="Overrides the recipe or variant image" class="sm:col-span-2">
          <input id="b-image" class="input font-mono" bind:value={image} placeholder={selected?.image || recipe.recipe.sandbox?.image || 'an image with the toolchain'} />
        </Field>
      {/if}
      <Field label="Ref" for="b-ref" hint="Tag, branch, or commit. Empty follows the recipe, usually the latest release">
        <input id="b-ref" class="input font-mono" bind:value={ref} placeholder={recipe.recipe.source?.ref || 'latest'} />
      </Field>
      <CheckCard bind:checked={force} class="self-end" title="Rebuild even when cached" description="An unchanged recipe on an unchanged host is a cache hit" />
      <Field label="Variables" for="b-vars" hint="One name=value per line, overriding recipe vars" class="sm:col-span-2">
        <textarea id="b-vars" class="input h-20" bind:value={varsText} placeholder={pairsText(recipe.vars).split('\n').slice(0, 2).join('\n') || 'build_type=Release'}></textarea>
      </Field>
    </div>

    {#if recipe.missingTools.length || recipe.unmet.length}
      <div class="mt-4 rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-xs leading-5 text-warn">
        {#if recipe.missingTools.length}<div>Missing tools: {recipe.missingTools.join(', ')}</div>{/if}
        {#each recipe.unmet as u (u)}<div>{u}</div>{/each}
      </div>
    {/if}
    {#if recipe.recipe.steps.length}
      <details class="mt-4 rounded-lg border border-line">
        <summary class="cursor-pointer px-3 py-2 text-xs font-medium text-fg-muted select-none">{recipe.recipe.steps.length} steps</summary>
        <ol class="border-t border-line px-3 py-2 text-xs">
          {#each recipe.recipe.steps as s, i (i)}
            <li class="flex gap-2 py-1"><span class="w-4 text-fg-faint tabular-nums">{i + 1}</span><span class="text-fg">{s.name}</span><span class="ml-auto truncate font-mono text-fg-faint" title={s.command.join(' ')}>{s.command[0]}</span></li>
          {/each}
        </ol>
      </details>
    {/if}
  {/if}
</FormDialog>
