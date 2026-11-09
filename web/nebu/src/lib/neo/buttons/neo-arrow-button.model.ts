import type { NeoButtonProps } from '#lib/neo/buttons/neo-button.model.js';
import type { NeoIconArrowProps } from '#lib/neo/icons/neo-icon.model.js';

export type NeoArrowButtonProps = NeoButtonProps & {
  /**
   * Optional arrow properties.
   */
  arrowProps?: NeoIconArrowProps;
  /**
   * Arrow direction.
   */
  direction?: NeoIconArrowProps['direction'];
};
