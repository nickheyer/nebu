import type { NeoButtonProps } from '#lib/neo/buttons/neo-button.model.js';

export interface NeoThemeSelectorProps extends NeoButtonProps {
  /**
   * The label to show in the theme selector.
   *
   * @default Theme
   */
  label?: string;
} ;
