import { Code } from '@connectrpc/connect';
import { api, code, message } from './api';
import { confirm } from './confirm.svelte';
import { fail, ok } from './toast.svelte';
import type { Slot } from '$proto/slot_pb';
import type { StoredModel } from '$proto/store_pb';

// Dialog state shared by every page that shows slots
export const slotUi = $state({
  runOpen: false,
  runModel: null as StoredModel | null,
  runSlotId: '',
  editOpen: false,
  editSlot: null as Slot | null
});

// Opens the run dialog for a model, into a slot when given
export function runModel(model: StoredModel | null, slotId = '') {
  slotUi.runModel = model;
  slotUi.runSlotId = slotId;
  slotUi.runOpen = true;
}

// Opens the run dialog aimed at a slot
export function swapSlot(slot: Slot) {
  runModel(null, slot.id);
}

export function editSlot(slot: Slot) {
  slotUi.editSlot = slot;
  slotUi.editOpen = true;
}

export function newSlot() {
  slotUi.editSlot = null;
  slotUi.editOpen = true;
}

// Deletes a slot after confirming, forcing past what the daemon names when asked
export async function deleteSlot(slot: Slot): Promise<boolean> {
  const yes = await confirm({ title: `Delete ${slot.name}?`, message: 'Clients using this name get 404. Stored models stay.', action: 'Delete', tone: 'bad' });
  if (!yes) return false;
  try {
    await api.slots.deleteSlot({ id: slot.id, force: false });
    ok(`Deleted ${slot.name}`);
    return true;
  } catch (err) {
    const c = code(err);
    const ask =
      c === Code.InvalidArgument
        ? { title: `Stop ${slot.name} and delete it?`, message: message(err), action: 'Stop and delete' }
        : c === Code.FailedPrecondition
          ? { title: `Delete ${slot.name} and what swaps into it?`, message: message(err), action: 'Delete all' }
          : null;
    if (!ask) {
      fail(err, 'Delete failed');
      return false;
    }
    if (!(await confirm({ ...ask, tone: 'bad' }))) return false;
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

// Stops the occupant, keeping the slot
export async function evictSlot(slot: Slot): Promise<boolean> {
  const yes = await confirm({ title: `Evict ${slot.name}?`, message: 'The model stops. The slot stays.', action: 'Evict', tone: 'bad' });
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

// Stops an instance after confirming
export async function stopInstance(id: string, name: string): Promise<boolean> {
  const yes = await confirm({ title: `Stop ${name}?`, message: 'It stays stopped after a daemon restart.', action: 'Stop', tone: 'bad' });
  if (!yes) return false;
  try {
    await api.instances.stopInstance({ id });
    ok(`Stopping ${name}`);
    return true;
  } catch (err) {
    fail(err, 'Stop failed');
    return false;
  }
}
