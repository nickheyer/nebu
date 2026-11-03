import type { NeoButtonProps } from '#lib/neo/buttons/neo-button.model.js';
import type { NeoInputProps } from '#lib/neo/inputs/common/neo-input.model.js';

export type NeoDateTimeProps = {
  /**
   * Custom icon for the picker.
   */
  icon?: NeoButtonProps['icon'];
  /**
   * Button properties to pass to the increment and decrement buttons.
   */
  buttonProps?: NeoButtonProps;
} & NeoInputProps;
