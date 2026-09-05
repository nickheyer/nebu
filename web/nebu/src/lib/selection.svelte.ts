import { page } from '$app/state';
import { replaceState } from '$app/navigation';

// A page's drawer id, seeded from the URL and dropped from it on close
export function selectionParam(path: string, key = 'id') {
  const sel = $state({ id: page.url.searchParams.get(key) ?? '' });
  $effect(() => {
    const q = page.url.searchParams.get(key);
    if (q) sel.id = q;
  });
  $effect(() => {
    if (!sel.id && page.url.searchParams.has(key)) replaceState(path, {});
  });
  return sel;
}
