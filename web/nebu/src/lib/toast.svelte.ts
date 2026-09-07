import { message } from './api';
import type { Tone } from './format';

export interface Toast {
  id: number;
  tone: Tone;
  title: string;
  detail?: string;
  href?: string;
  linkLabel?: string;
  sticky?: boolean;
  // Something is still happening, drawn with a spinner instead of a tone icon
  busy?: boolean;
}

export const toasts = $state<Toast[]>([]);
let next = 1;
const timers = new Map<number, ReturnType<typeof setTimeout>>();

// Dismisses a toast after a moment, longer for a failure
function expire(id: number, tone: Tone) {
  const t = timers.get(id);
  if (t) clearTimeout(t);
  timers.set(id, setTimeout(() => dismiss(id), tone === 'bad' ? 9000 : 4500));
}

// Shows a toast, dismissed after a moment unless sticky
export function toast(t: Omit<Toast, 'id'>): number {
  const id = next++;
  toasts.push({ id, ...t });
  if (!t.sticky) expire(id, t.tone);
  return id;
}

// Turns a shown toast into its ending; one already gone is shown again as its ending
export function settle(id: number, t: Omit<Toast, 'id' | 'busy' | 'sticky'>) {
  const i = toasts.findIndex((x) => x.id === id);
  if (i < 0) {
    toast(t);
    return;
  }
  toasts[i] = { id, ...t };
  expire(id, t.tone);
}

export function dismiss(id: number) {
  const timer = timers.get(id);
  if (timer) clearTimeout(timer);
  timers.delete(id);
  const i = toasts.findIndex((t) => t.id === id);
  if (i >= 0) toasts.splice(i, 1);
}

// Reports an error from an RPC or anything else
export function fail(err: unknown, title = 'Something went wrong') {
  toast({ tone: 'bad', title, detail: message(err) });
}

export function ok(title: string, detail?: string, link?: { href: string; label: string }) {
  toast({ tone: 'ok', title, detail, href: link?.href, linkLabel: link?.label });
}

// Reports that something has started and is still running
export function started(title: string, detail?: string, link?: { href: string; label: string }): number {
  return toast({ tone: 'accent', busy: true, title, detail, href: link?.href, linkLabel: link?.label });
}
