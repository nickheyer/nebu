<script lang="ts">
  import type { NeoSourceSelectorProps } from '#lib/neo/providers/neo-source-selector.model.js';

  import NeoButton from '#lib/neo/buttons/NeoButton.svelte';
  import NeoIconCircle from '#lib/neo/icons/NeoIconCircle.svelte';
  import NeoIconCube from '#lib/neo/icons/NeoIconCube.svelte';
  import NeoIconEmpty from '#lib/neo/icons/NeoIconEmpty.svelte';
  import NeoIconWave from '#lib/neo/icons/NeoIconWave.svelte';
  import { useNeoThemeContext } from '#lib/neo/providers/neo-theme-provider-context.svelte.js';
  import { NeoTransition } from '#lib/neo/providers/neo-theme-provider.model.js';

  const {
    // state
    label = 'Transition',

    // Button props
    ref = $bindable(),
    hovered = $bindable(false),
    focused = $bindable(false),

    // Other props
    ...rest
  }: NeoSourceSelectorProps = $props();

  const context = useNeoThemeContext();

  const transition = $derived(context.transition);

  const transitions = Object.values(NeoTransition);
  const sourceMap = { ...transitions };
  const indexMap = Object.fromEntries(Object.entries(sourceMap).map(([k, v]) => [v, Number(k)]));

  const onclick = () => {
    context.update({ transition: sourceMap[(indexMap[transition] + 1) % transitions.length] });
  };

</script>

<NeoButton
  aria-label="Cycle theme transition"
  title="Cycle theme transition - {transition}"
  {onclick}
  {label}
  {...rest}
>
  {#snippet icon()}
    {#if transition === NeoTransition.Spin}
      <NeoIconCube />
    {:else if transition === NeoTransition.Wave}
      <NeoIconWave />
    {:else if transition === NeoTransition.Circle}
      <NeoIconCircle />
    {:else}
      <NeoIconEmpty />
    {/if}
  {/snippet}
</NeoButton>
