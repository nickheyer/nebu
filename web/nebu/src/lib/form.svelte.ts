import { untrack } from 'svelte';
import { fail, ok } from './toast.svelte';

// What a submit resolves to on success, the toast announcing it
export interface Done {
  title: string;
  detail?: string;
  link?: { href: string; label: string };
}

export interface FormSpec {
  open: () => boolean;
  close: () => void;
  // Runs on every opening so the fields start from the props
  reset: () => void;
  // Sends the form, resolving to the success toast, or to nothing when it toasted itself
  submit: () => Promise<Done | void>;
  // Title of the failure toast, left out when submit reports refusals itself
  failTitle?: string | (() => string);
}

// Wires the reset on open, the saving flag, and the submit path every dialog shares
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
