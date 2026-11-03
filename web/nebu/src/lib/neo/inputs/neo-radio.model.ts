import type { NeoCommonRadioProps, NeoRadioButtonProps } from '#lib/neo/buttons/neo-radio-button.model.js';
import type { NeoBaseInputProps, NeoInputProps } from '#lib/neo/inputs/common/neo-input.model.js';
import type { NeoLabelProps } from '#lib/neo/inputs/common/neo-label.model.js';

export type NeoRadioProps = NeoBaseInputProps
  & Pick<
    NeoInputProps,
    | 'readonly'
    | 'loading'
    | 'hovered'
    | 'in'
    | 'out'
    | 'transition'
    | 'labelRef'
    | 'labelProps'
    | 'containerRef'
    | 'containerProps'
    | 'color'
    | 'tinted'
    | 'flex'
    | 'width'
    | 'height'
  >
  & Pick<NeoLabelProps, 'label'>
  & NeoCommonRadioProps & {
    /**
     * Props to be passed to the radio button.
     */
    buttonProps?: NeoRadioButtonProps;
  };
