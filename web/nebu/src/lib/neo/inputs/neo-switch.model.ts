import type { NeoCommonSwitchProps, NeoSwitchButtonProps } from '#lib/neo/buttons/neo-switch-button.model.js';
import type { NeoBaseInputProps, NeoInputProps } from '#lib/neo/inputs/common/neo-input.model.js';
import type { NeoLabelProps } from '#lib/neo/inputs/common/neo-label.model.js';
import type { NeoValidationProps } from '#lib/neo/inputs/common/neo-validation.model.js';

/**
 * Note: Checkbox group are exclusive due to the api design.
 *
 * @see [github issue #2308](https://github.com/sveltejs/svelte/issues/2308)
 */
export type NeoSwitchProps = NeoBaseInputProps
  & Pick<
    NeoInputProps,
    | 'id'
    | 'readonly'
    | 'loading'
    | 'validation'
    | 'register'
    | 'hovered'
    | 'in'
    | 'out'
    | 'transition'
    | 'labelRef'
    | 'labelProps'
    | 'containerRef'
    | 'containerProps'
    | 'validationRef'
    | 'validationProps'
    | 'color'
    | 'tinted'
    | 'flex'
    | 'width'
    | 'height'
  >
  & NeoCommonSwitchProps
  & Pick<NeoLabelProps, 'label'>
  & Pick<NeoValidationProps, 'error' | 'message' | 'messageProps'> & {
    /**
     * Props to be passed to the switch button.
     */
    buttonProps?: NeoSwitchButtonProps;
  };
