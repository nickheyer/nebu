import { api } from './api';
import { live, instanceLive } from './state.svelte';
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

// Opens the run dialog aimed at a slot, letting the person pick a model
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

// Deletes a slot after confirming, stopping its occupant
export async function deleteSlot(slot: Slot): Promise<boolean> {
  const occupied = !!slot.instanceId && instanceLive(live.instances.get(slot.instanceId));
  const yes = await confirm({
    title: `Delete ${slot.name}?`,
    message: occupied
      ? `The instance serving ${slot.request?.repo ?? 'it'} stops and the public name ${slot.name} stops answering.`
      : `The public name ${slot.name} stops answering. Stored models are untouched.`,
    action: 'Delete',
    tone: 'bad'
  });
  if (!yes) return false;
  try {
    await api.slots.deleteSlot({ id: slot.id, force: true });
    ok(`Deleted ${slot.name}`);
    return true;
  } catch (err) {
    fail(err, 'Delete failed');
    return false;
  }
}

// Stops the occupant, keeping the slot
export async function evictSlot(slot: Slot): Promise<boolean> {
  const yes = await confirm({ title: `Evict ${slot.name}?`, message: 'The instance stops. The slot and its public name stay, answering 503 until something serves it again.', action: 'Evict', tone: 'bad' });
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
