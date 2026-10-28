import { readLocal, writeLocal } from './persist';

const key = 'nebu.chat.theater';

// Theater mode hides the app chrome so the conversation fills the window,
// like a video player's theater view. It is a display preference of this
// browser, not of any one chat, and is remembered between visits.
export const theater = $state({ on: false });

export function loadTheater() {
  theater.on = readLocal(key) === '1';
}

export function setTheater(on: boolean) {
  theater.on = on;
  writeLocal(key, on ? '1' : '');
}

export function toggleTheater() {
  setTheater(!theater.on);
}
