// Focus on / unless the user is typing in another field.
export function slashFocus(node: HTMLElement) {
  const onKey = (e: KeyboardEvent) => {
    if (e.key !== '/' || e.metaKey || e.ctrlKey || e.altKey) return;
    const tag = document.activeElement?.tagName ?? '';
    if (['INPUT', 'TEXTAREA', 'SELECT'].includes(tag) || (document.activeElement as HTMLElement | null)?.isContentEditable) return;
    e.preventDefault();
    node.focus();
  };
  window.addEventListener('keydown', onKey);
  return {
    destroy() {
      window.removeEventListener('keydown', onKey);
    }
  };
}
