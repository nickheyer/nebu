import type { Tone } from './format';

export interface ConfirmRequest {
  title: string;
  message?: string;
  action?: string;
  tone?: Tone;
}

interface Pending extends ConfirmRequest {
  resolve: (ok: boolean) => void;
}

export const pending = $state<{ current: Pending | null }>({ current: null });

export function confirm(req: ConfirmRequest): Promise<boolean> {
  return new Promise((resolve) => {
    pending.current?.resolve(false);
    pending.current = { ...req, resolve };
  });
}

export function answer(ok: boolean) {
  pending.current?.resolve(ok);
  pending.current = null;
}
