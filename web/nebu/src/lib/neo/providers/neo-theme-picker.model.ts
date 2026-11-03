import type { Snippet } from 'svelte';

import type { NeoButtonGroupProps } from '#lib/neo/buttons/neo-button-group.model.js';
import type { INeoThemeProviderContext } from '#lib/neo/providers/neo-theme-provider.model.js';

export type NeoThemePickerProps = {
  // Snippet

  /**
   * Any children to display in the theme picker.
   */
  children?: Snippet<[INeoThemeProviderContext]>;

  // State

  /**
   * The label to show in the background picker.
   *
   * @default Background
   */
  labelBackground?: string;
  /**
   * The label to show in the text selector.
   *
   * @default Text
   */
  labelText?: string;
} & Omit<NeoButtonGroupProps, 'children'>;
