import { untrack } from 'svelte';
import { fail, ok } from './toast.svelte';

export interface Done {
  title: string;
  detail?: string;
  link?: { href: string; label: string };
}

export interface FormSpec {
  open: () => boolean;
  close: () => void;
  // Reset fields from props when the dialog opens.
  reset: () => void;
  // Return a success toast, or nothing if submit already showed one.
  submit: () => Promise<Done | void>;
  // Omit if submit handles errors.
  failTitle?: string | (() => string);
}

export function createForm(spec: FormSpec) {
  let saving = $state(false);
  $effect(() => {
    if (spec.open()) untrack(spec.reset);
  });
  async function run() {
    saving = true;
    try {
      const done = await spec.submit();
      if (done) ok(done.title, done.detail, done.link);
      spec.close();
    } catch (err) {
      const title = typeof spec.failTitle === 'function' ? spec.failTitle() : spec.failTitle;
      if (title) fail(err, title);
    } finally {
      saving = false;
    }
  }
  return {
    get saving() {
      return saving;
    },
    run
  };
}
