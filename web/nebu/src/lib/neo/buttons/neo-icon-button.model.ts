import type { NeoButtonProps } from '#lib/neo/buttons/neo-button.model.js';
import type { NeoIconProps } from '#lib/neo/icons/neo-icon.model.js';

export type NeoIconButtonProps = NeoButtonProps & {
  /**
   * Optional icon properties.
   */
  iconProps?: NeoIconProps;
};
