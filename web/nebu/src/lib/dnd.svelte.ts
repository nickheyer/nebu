const mime = 'text/nebu-model';

// The stored model key being dragged, empty when nothing is
export const dnd = $state({ model: '' });

// Starts dragging a stored model by its key
export function dragModel(ev: DragEvent, key: string) {
  ev.dataTransfer?.setData(mime, key);
  ev.dataTransfer?.setData('text/plain', key);
  if (ev.dataTransfer) ev.dataTransfer.effectAllowed = 'copyMove';
  dnd.model = key;
}

export function endDrag() {
  dnd.model = '';
}

// Reads the model key from a drop, empty when the drop was something else
export function droppedModel(ev: DragEvent): string {
  return ev.dataTransfer?.getData(mime) ?? '';
}

export function acceptsModel(ev: DragEvent): boolean {
  return !!ev.dataTransfer?.types.includes(mime);
}
