// One open floating surface, a dialog, drawer, menu, option list or tooltip
export interface NeoLayer {
  contains: (target: Node) => boolean;
  transient?: boolean;
}

const stack: NeoLayer[] = [];
const frozen = new WeakMap<Event, NeoLayer[]>();

// The open surfaces as they stood when an event first reached one of them
function during(event: Event): NeoLayer[] {
  let layers = frozen.get(event);
  if (!layers) {
    layers = [...stack];
    frozen.set(event, layers);
  }
  return layers;
}

// Puts a surface above every open one and returns the call that takes it off
export function openLayer(layer: NeoLayer): () => void {
  stack.push(layer);
  return () => {
    const at = stack.indexOf(layer);
    if (at >= 0) stack.splice(at, 1);
  };
}

// Whether Escape or an outside press is for this surface, a hover hint or the topmost of the rest
export function ownsDismiss(layer: NeoLayer, event: Event): boolean {
  if (layer.transient) return true;
  return during(event).findLast(l => !l.transient) === layer;
}

// Whether a node sits in a surface opened after this one
export function inLayerAbove(layer: NeoLayer, target: EventTarget | null, event?: Event): boolean {
  if (!(target instanceof Node)) return false;
  const layers = event ? during(event) : stack;
  const at = layers.indexOf(layer);
  return at >= 0 && layers.slice(at + 1).some(l => l.contains(target));
}
