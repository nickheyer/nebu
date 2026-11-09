import type { Snippet } from 'svelte';
import type { HTMLButtonAttributes } from 'svelte/elements';

import type { NeoInputProps } from '#lib/neo/inputs/common/neo-input.model.js';
import type { ShadowShallowElevation, ShadowShallowElevationString } from '#lib/neo/utils/shadow.utils.js';

export type NeoRadioElevation = ShadowShallowElevation | ShadowShallowElevationString;

export type NeoCommonRadioProps = Pick<
  NeoInputProps,
  'checked' | 'touched' | 'disabled' | 'start' | 'glass' | 'rounded' | 'skeleton' | 'tinted' | 'color'
> & {
  /**
   * Input elevation.
   * @default 2
   */
  elevation?: NeoRadioElevation;
};

export type NeoRadioButtonProps = NeoCommonRadioProps
  & HTMLButtonAttributes & {
    /**
     * Any content to be displayed inside the radio button.
     */
    children?: Snippet;
  };
