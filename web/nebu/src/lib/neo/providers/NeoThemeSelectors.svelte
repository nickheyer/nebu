<script lang="ts">
  import type { NeoThemeSelectorsProps } from '#lib/neo/providers/neo-theme-selectors.model.js';

  import NeoButtonGroup from '#lib/neo/buttons/NeoButtonGroup.svelte';
  import { useNeoThemeContext } from '#lib/neo/providers/neo-theme-provider-context.svelte.js';
  import NeoRememberSelector from '#lib/neo/providers/NeoRememberSelector.svelte';
  import NeoResetSelector from '#lib/neo/providers/NeoResetSelector.svelte';
  import NeoSourceSelector from '#lib/neo/providers/NeoSourceSelector.svelte';
  import NeoThemeSelector from '#lib/neo/providers/NeoThemeSelector.svelte';
  import NeoTransitionSelector from '#lib/neo/providers/NeoTransitionSelector.svelte';

  const {
    // Snippet
    children,

    // state
    sourceLabel,
    source: showSource,
    themeLabel,
    theme: showTheme = true,
    resetLabel,
    reset: showReset,
    rememberLabel,
    remember: showRemember,
    themeTransitionLabel,
    themeTransition: showTransition,

    // Other props
    themeTransitionProps,
    rememberProps,
    sourceProps,
    themeProps,
    resetProps,
    ...rest
  }: NeoThemeSelectorsProps = $props();

  const context = useNeoThemeContext();
</script>

<NeoButtonGroup elevation="2" {...rest}>
  {#if showSource}
    <NeoSourceSelector label={sourceLabel} {...sourceProps} />
  {/if}
  {#if showTheme}
    <NeoThemeSelector label={themeLabel} {...themeProps} />
  {/if}
  {#if showTransition}
    <NeoTransitionSelector label={themeTransitionLabel} {...themeTransitionProps} />
  {/if}
  {#if showReset}
    <NeoResetSelector label={resetLabel} {...resetProps} />
  {/if}
  {#if showRemember}
    <NeoRememberSelector label={rememberLabel} {...rememberProps} />
  {/if}
  {@render children?.(context.state)}
</NeoButtonGroup>
