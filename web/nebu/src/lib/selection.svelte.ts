import { page } from '$app/state';
import { replaceState } from '$app/navigation';

// A page's drawer id, seeded from ?id= and dropped from the URL on close
export function selectionParam(path: string) {
  const sel = $state({ id: page.url.searchParams.get('id') ?? '' });
  $effect(() => {
    const q = page.url.searchParams.get('id');
    if (q) sel.id = q;
  });
  $effect(() => {
    if (!sel.id && page.url.searchParams.has('id')) replaceState(path, {});
  });
  return sel;
}
