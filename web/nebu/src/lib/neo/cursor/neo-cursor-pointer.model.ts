import type { Snippet } from 'svelte';

import type { NeoCursorContext } from '#lib/neo/cursor/neo-cursor.model.js';
import type { HTMLNeoBaseElement } from '#lib/neo/utils/html-element.utils.js';

export type NeoCursorPointerProps = NeoCursorContext & {
  /**
   * Inner content of the cursor.
   */
  children?: Snippet<[NeoCursorContext]>;
} & Omit<HTMLNeoBaseElement<HTMLSpanElement>, 'children'>;
