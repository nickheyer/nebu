import { Code } from '@connectrpc/connect';
import { api, code, message } from './api';
import { confirm } from './confirm.svelte';
import { fail, ok } from './toast.svelte';
import { startedTask, startedStop } from './state.svelte';
import type { Slot } from '$proto/slot_pb';
import type { StoredModel } from '$proto/store_pb';

// The run dialog, shared by every page that can start a model
export const runUi = $state({
  open: false,
  model: null as StoredModel | null,
  slotId: ''
});

// Opens the run dialog for a model, aimed at a slot when given
export function runModel(model: StoredModel | null, slotId = '') {
  runUi.model = model;
  runUi.slotId = slotId;
  runUi.open = true;
}

// Opens the run dialog aimed at a slot
export function swapSlot(slot: Slot) {
  runModel(null, slot.id);
}

// Deletes a slot after confirming, stopping its occupant when the daemon asks for that
export async function deleteSlot(slot: Slot): Promise<boolean> {
  const yes = await confirm({ title: `Delete slot ${slot.name}?`, message: `Requests for "${slot.name}" will get 404. Stored models are not affected.`, action: 'Delete', tone: 'bad' });
  if (!yes) return false;
  try {
    await api.slots.deleteSlot({ id: slot.id, force: false });
    ok(`Deleted ${slot.name}`);
    return true;
  } catch (err) {
    if (code(err) !== Code.InvalidArgument) {
      fail(err, 'Delete failed');
      return false;
    }
    if (!(await confirm({ title: `Stop the model and delete ${slot.name}?`, message: message(err), action: 'Stop and delete', tone: 'bad' }))) return false;
    try {
      await api.slots.deleteSlot({ id: slot.id, force: true });
      ok(`Deleted ${slot.name}`);
      return true;
    } catch (again) {
      fail(again, 'Delete failed');
      return false;
    }
  }
}

// Stops the occupant and clears the slot's model
export async function evictSlot(slot: Slot): Promise<boolean> {
  const yes = await confirm({ title: `Evict ${slot.name}?`, message: 'The model stops and the slot forgets it. The slot and its name stay.', action: 'Evict', tone: 'bad' });
  if (!yes) return false;
  try {
    await api.slots.evictSlot({ id: slot.id });
    ok(`Evicted ${slot.name}`);
    return true;
  } catch (err) {
    fail(err, 'Evict failed');
    return false;
  }
}

// Runs the slot's model again
export async function relaunchSlot(slot: Slot): Promise<boolean> {
  try {
    const r = await api.slots.relaunchSlot({ id: slot.id });
    startedTask(`Relaunching ${slot.name}`, `Relaunched ${slot.name}`, slot.request?.repo, r.task);
    return true;
  } catch (err) {
    fail(err, 'Relaunch refused');
    return false;
  }
}

// Stops an instance after confirming
export async function stopInstance(id: string, name: string): Promise<boolean> {
  const yes = await confirm({ title: `Stop ${name}?`, message: 'It will not come back after a daemon restart.', action: 'Stop', tone: 'bad' });
  if (!yes) return false;
  try {
    await api.instances.stopInstance({ id });
    startedStop(id, name);
    return true;
  } catch (err) {
    fail(err, 'Stop failed');
    return false;
  }
}
