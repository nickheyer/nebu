import { untrack } from 'svelte';
import { page } from '$app/state';
import { replaceState } from '$app/navigation';

// The active tab of a page, held as state so switching re-renders at once
//
// It starts from ?tab= when the URL names one, follows a later link that
// names one, and writes itself back to the URL so a reload or a copied link
// opens the same tab. The first tab is the default and needs no query.
export function tabState(ids: () => string[], fallback: () => string) {
  const fromUrl = () => {
    const q = page.url.searchParams.get('tab');
    return q && ids().includes(q) ? q : '';
  };
  const tab = $state({ value: fromUrl() || fallback() });
  $effect(() => {
    const q = fromUrl();
    if (q) untrack(() => (tab.value = q));
  });
  $effect(() => {
    const v = tab.value;
    untrack(() => {
      const url = new URL(window.location.href);
      const want = v === ids()[0] ? '' : v;
      if ((url.searchParams.get('tab') ?? '') === want) return;
      if (want) url.searchParams.set('tab', want);
      else url.searchParams.delete('tab');
      replaceState(url, {});
    });
  });
  return tab;
}
