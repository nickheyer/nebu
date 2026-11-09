import type { NeoButtonProps } from '#lib/neo/buttons/neo-button.model.js';

export interface NeoSourceSelectorProps extends NeoButtonProps {
  /**
   * The label to show in the source selector.
   *
   * @default Source
   */
  label?: string;
} ;
