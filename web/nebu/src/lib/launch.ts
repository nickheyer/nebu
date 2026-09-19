import { api } from './api';
import { live, instanceLive, startedTask } from './state.svelte';
import { fail } from './toast.svelte';

export interface RunSpec {
  sourceId: string;
  repo: string;
  group: string;
  runtimeId?: string;
  installId?: string;
  name?: string;
  params?: Record<string, string>;
  slotId?: string;
  // Override the plan refusal and repeat preparation.
  force?: boolean;
}

export function slotOccupied(slotId: string | undefined): boolean {
  if (!slotId) return false;
  const s = live.slots.get(slotId);
  return !!s?.instanceId && instanceLive(live.instances.get(s.instanceId));
}

// Swap if the slot is occupied. Return the task ID.
export async function launch(spec: RunSpec, drainFirst = false, refused?: (err: unknown) => void): Promise<string | undefined> {
  const run = { ...spec, params: spec.params ?? {} };
  const swap = slotOccupied(spec.slotId);
  try {
    const task = swap ? (await api.slots.swap({ slotId: spec.slotId!, run, drainFirst })).task : (await api.instances.run(run)).task;
    const id = task?.id ?? '';
    const target = spec.slotId ? ` in ${live.slots.get(spec.slotId)?.name ?? 'slot'}` : '';
    const what = `${spec.repo.split('/').pop()}${target}`;
    startedTask(`${swap ? 'Swapping' : 'Starting'} ${what}`, `${swap ? 'Swapped' : 'Started'} ${what}`, spec.group, task);
    return id;
  } catch (err) {
    fail(err, swap ? 'Swap refused' : 'Run refused');
    refused?.(err);
    return undefined;
  }
}
