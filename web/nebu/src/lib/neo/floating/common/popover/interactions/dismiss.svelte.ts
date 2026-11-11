import type { NeoLayer } from '../../layers.js';
import type { DismissOptions, Interaction } from '../popover.types.js';

import { inLayerAbove, openLayer, ownsDismiss } from '../../layers.js';

/**
 * Locked dismiss contract:
 *  - Esc closes (document keydown, capture phase), the topmost surface only.
 *  - Pointerdown outside reference + floating closes (document pointerdown,
 *    capture phase).
 *  - Pointerdown on the reference (or a descendant) does NOT close — that
 *    keeps click-to-toggle authoritative.
 *  - Pointerdown on the floating (or a descendant) does NOT close — portal
 *    safety: containment is checked via `Node.contains`, which walks the
 *    DOM ancestry and resolves correctly even when the floating is portaled
 *    out of the reference subtree.
 *  - Pointerdown on a surface opened after this one does NOT close.
 *  - Ancestor scroll does NOT close. Position pipeline already follows the
 *    reference; closing on every scroll surprises users.
 *
 * Listeners are bound only on the open transition (via `onOpenChange`,
 * dispatched synchronously by `Popover`). The cleanup returned by
 * `onOpenChange(true, …)` is invoked on the matching close transition. The
 * `enabled` flag is gated inside the handlers so flipping it at runtime
 * short-circuits without re-binding.
 */
export function dismiss(options: DismissOptions = {}): Interaction {
  return (ctx) => {
    const enabled = $derived(options.enabled ?? true);

    function isInside(target: EventTarget | null): boolean {
      if (!(target instanceof Node)) return false;
      const reference = ctx.popover.referenceEl;
      const floating = ctx.popover.floatingEl;
      if (reference && (reference === target || reference.contains(target))) return true;
      if (floating && (floating === target || floating.contains(target))) return true;
      return false;
    }

    const layer: NeoLayer = {
      contains: isInside,
      get transient() {
        return options.transient ?? false;
      },
    };

    function onKeyDown(event: KeyboardEvent): void {
      if (!enabled || event.key !== 'Escape' || !ownsDismiss(layer, event)) return;
      ctx.onOpenChange(false, event, 'escape-key');
    }

    function onPointerDown(event: PointerEvent): void {
      if (!enabled || isInside(event.target) || inLayerAbove(layer, event.target, event)) return;
      ctx.onOpenChange(false, event, 'outside-press');
    }

    return {
      onOpenChange(open: boolean) {
        if (!open) return;
        const closeLayer = openLayer(layer);
        const doc = ctx.popover.floatingEl?.ownerDocument ?? document;
        doc.addEventListener('keydown', onKeyDown, true);
        doc.addEventListener('pointerdown', onPointerDown, true);
        return () => {
          closeLayer();
          doc.removeEventListener('keydown', onKeyDown, true);
          doc.removeEventListener('pointerdown', onPointerDown, true);
        };
      },
    };
  };
}
