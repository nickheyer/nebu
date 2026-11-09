<script lang="ts">
  import type { NeoIconThemeProps } from '#lib/neo/icons/neo-icon.model.js';

  import NeoIconSunMoon from '#lib/neo/icons/NeoIconSunMoon.svelte';
  import { useNeoThemeContext } from '#lib/neo/providers/neo-theme-provider-context.svelte.js';
  import { NeoTheme } from '#lib/neo/providers/neo-theme-provider.model.js';

  const { ...rest }: NeoIconThemeProps = $props();

  const context = useNeoThemeContext();
  const dark = $derived(context.theme === NeoTheme.Dark);
</script>

<NeoIconSunMoon state={dark ? 'moon' : 'sun'} {...rest} />
