import type { Snippet } from 'svelte';

import type { TabId } from '#lib/neo/nav/neo-tab.model.js';
import type { NeoTabsContext } from '#lib/neo/nav/neo-tabs-context.svelte.js';
import type { HTMLTransitionProps } from '#lib/neo/utils/action.utils.js';
import type { HTMLNeoBaseElement, HTMLRefProps } from '#lib/neo/utils/html-element.utils.js';

export type NeoTabPanelProps<Id extends TabId, Value = unknown, Tag extends keyof HTMLElementTagNameMap = 'div'> = {
  // Snippets

  /**
   * Snippet to display as the tab content.
   */
  children?: Snippet<[NeoTabsContext<Id, Value> | undefined]>;

  // States

  /**
   * The tab id to associate with this panel.
   */
  tabId?: Id;
  /**
   * If `true`, the pane will appear when no other tabs are active.
   */
  empty?: boolean;
  /**
   * The HTML tag to use for the container.
   *
   * @default 'div'
   * @see animate
   */
  tag?: Tag | keyof HTMLElementTagNameMap;

  // Styles
  /**
   * If `true`, pane transition will be animated.
   * If `false`, the panel will check if any neo-tabs-card parent is animating.
   * @default false
   */
  animate?: boolean;
} & HTMLNeoBaseElement<HTMLElementTagNameMap[Tag]>
& HTMLRefProps
& HTMLTransitionProps;
