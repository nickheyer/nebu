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
}

export const toasts = $state<Toast[]>([]);
let next = 1;

// Shows a toast, dismissed after a moment unless sticky
export function toast(t: Omit<Toast, 'id'>): number {
  const id = next++;
  toasts.push({ id, ...t });
  if (!t.sticky) setTimeout(() => dismiss(id), t.tone === 'bad' ? 9000 : 4500);
  return id;
}

export function dismiss(id: number) {
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

// Runs an operation, toasting on failure, returning undefined then
export async function attempt<T>(title: string, fn: () => Promise<T>): Promise<T | undefined> {
  try {
    return await fn();
  } catch (err) {
    fail(err, title);
    return undefined;
  }
}
