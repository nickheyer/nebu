import { Code } from '@connectrpc/connect';
import { api, code, message } from './api';
import { confirm } from './confirm.svelte';
import { fail, ok } from './toast.svelte';
import type { Slot } from '$proto/slot_pb';
import type { StoredModel } from '$proto/store_pb';

// The run panel, shared by every page that can start a model
export const runUi = $state({
  open: false,
  model: null as StoredModel | null,
  slotId: ''
});

// Opens the run panel for a model, aimed at a slot when given
export function runModel(model: StoredModel | null, slotId = '') {
  runUi.model = model;
  runUi.slotId = slotId;
  runUi.open = true;
}

// Opens the run panel aimed at a slot
export function swapSlot(slot: Slot) {
  runModel(null, slot.id);
}

// Deletes a slot after confirming, stopping its occupant when the daemon asks for that
export async function deleteSlot(slot: Slot): Promise<boolean> {
  const yes = await confirm({ title: `Delete ${slot.name}?`, message: 'Clients using this name get 404. Stored models stay.', action: 'Delete', tone: 'bad' });
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
    if (!(await confirm({ title: `Stop ${slot.name} and delete it?`, message: message(err), action: 'Stop and delete', tone: 'bad' }))) return false;
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
