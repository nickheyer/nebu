// Store image metadata in localStorage and bytes in IndexedDB to avoid the localStorage limit.

import { localKeys, readLocal } from './persist';

export interface Attachment {
  id: string;
  mediaType: string;
  width: number;
  height: number;
  bytes: number;
  name: string;
}

// Vision encoders downscale or tile images beyond this edge length.
export const maxEdge = 1568;
// Convert larger PNGs to JPEG to limit request size.
const maxPngBytes = 4 << 20;
const jpegQuality = 0.9;

const dbName = 'nebu';
const storeName = 'images';
// Base-36 timestamp width, valid through 5188.
const idTimeLength = 9;

// Prefix IDs with their creation time. Avoid crypto.randomUUID outside secure contexts.
export function newId(): string {
  return Date.now().toString(36).padStart(idTimeLength, '0') + Math.random().toString(36).slice(2, 10);
}

function idTime(id: string): number {
  return parseInt(id.slice(0, idTimeLength), 36);
}

function settle<T>(req: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error ?? new Error('IndexedDB request failed'));
  });
}

let db: Promise<IDBDatabase> | null = null;

function open(): Promise<IDBDatabase> {
  if (db) return db;
  const opening = new Promise<IDBDatabase>((resolve, reject) => {
    if (typeof indexedDB === 'undefined') {
      reject(new Error('This browser has no IndexedDB, which images are kept in'));
      return;
    }
    const req = indexedDB.open(dbName, 1);
    req.onupgradeneeded = () => req.result.createObjectStore(storeName);
    req.onsuccess = () => {
      const d = req.result;
      d.onclose = () => (db = null);
      d.onversionchange = () => {
        d.close();
        db = null;
      };
      resolve(d);
    };
    req.onerror = () => reject(req.error ?? new Error('IndexedDB refused to open'));
    req.onblocked = () => reject(new Error('IndexedDB is held open by another tab'));
  });
  db = opening;
  opening.catch(() => (db = null));
  return opening;
}

async function store(mode: IDBTransactionMode): Promise<IDBObjectStore> {
  return (await open()).transaction(storeName, mode).objectStore(storeName);
}

export async function putImage(id: string, blob: Blob): Promise<void> {
  await settle((await store('readwrite')).put(blob, id));
}

export async function getImage(id: string): Promise<Blob | undefined> {
  return (await settle((await store('readonly')).get(id))) as Blob | undefined;
}

export async function deleteImages(ids: string[]): Promise<void> {
  if (!ids.length) return;
  const s = await store('readwrite');
  await Promise.all(ids.map((id) => settle(s.delete(id))));
}

// Keep recent unreferenced images in case another tab is attaching them.
export async function sweepImages(keep: Set<string>, before: number): Promise<void> {
  const s = await store('readwrite');
  const keys = (await settle(s.getAllKeys())) as string[];
  await Promise.all(keys.filter((k) => !keep.has(k) && idTime(k) < before).map((k) => settle(s.delete(k))));
}

export async function prepareImage(file: Blob, name: string): Promise<{ attachment: Attachment; blob: Blob }> {
  if (!file.type.startsWith('image/')) throw new Error(`${name} is ${file.type || 'of no image type'}`);
  const bitmap = await decode(file, name);
  try {
    const scale = Math.min(1, maxEdge / Math.max(bitmap.width, bitmap.height));
    const keep = scale === 1 && (file.type === 'image/jpeg' || (file.type === 'image/png' && file.size <= maxPngBytes));
    if (keep) {
      return { attachment: { id: newId(), mediaType: file.type, width: bitmap.width, height: bitmap.height, bytes: file.size, name }, blob: file };
    }
    const width = Math.max(1, Math.round(bitmap.width * scale));
    const height = Math.max(1, Math.round(bitmap.height * scale));
    // Preserve PNG transparency unless the encoded image exceeds the size limit.
    let blob = await draw(bitmap, width, height, file.type === 'image/jpeg' ? 'image/jpeg' : 'image/png');
    if (blob.type === 'image/png' && blob.size > maxPngBytes) blob = await draw(bitmap, width, height, 'image/jpeg');
    return { attachment: { id: newId(), mediaType: blob.type, width, height, bytes: blob.size, name }, blob };
  } finally {
    bitmap.close();
  }
}

export async function storeBlob(blob: Blob, name: string): Promise<Attachment> {
  const attachment = { id: newId(), mediaType: blob.type, width: 0, height: 0, bytes: blob.size, name };
  await putImage(attachment.id, blob);
  return attachment;
}

export function referencedIds(): Set<string> {
  const keep = new Set<string>();
  for (const k of localKeys('nebu.chat.')) {
    try {
      const s = JSON.parse(readLocal(k)) as { turns?: { images?: { id: string }[]; media?: { id?: string }[] }[] } | null;
      for (const t of s?.turns ?? []) {
        for (const a of t.images ?? []) keep.add(a.id);
        for (const m of t.media ?? []) if (m.id) keep.add(m.id);
      }
    } catch {
      // Ignore unrelated keys sharing the prefix.
    }
  }
  try {
    const history = JSON.parse(readLocal('nebu.generate.history') || '[]') as { files?: { id: string }[]; inputs?: { id: string }[] }[];
    for (const g of history) for (const f of [...(g.files ?? []), ...(g.inputs ?? [])]) keep.add(f.id);
  } catch {
  }
  return keep;
}

// Keep unreferenced files for one day in case another tab is attaching them.
export function sweepStale(): Promise<void> {
  return sweepImages(referencedIds(), Date.now() - 24 * 60 * 60 * 1000);
}

export async function storeImage(blob: Blob, name: string): Promise<Attachment> {
  const bitmap = await decode(blob, name);
  const attachment = { id: newId(), mediaType: blob.type, width: bitmap.width, height: bitmap.height, bytes: blob.size, name };
  bitmap.close();
  await putImage(attachment.id, blob);
  return attachment;
}

async function decode(blob: Blob, name: string): Promise<ImageBitmap> {
  try {
    return await createImageBitmap(blob);
  } catch {
    throw new Error(`${name} could not be decoded as an image`);
  }
}

// Composite JPEGs on white because JPEG has no alpha channel.
async function draw(bitmap: ImageBitmap, width: number, height: number, type: 'image/png' | 'image/jpeg'): Promise<Blob> {
  const canvas = document.createElement('canvas');
  canvas.width = width;
  canvas.height = height;
  const ctx = canvas.getContext('2d');
  if (!ctx) throw new Error('The browser gave no canvas to scale the image with');
  if (type === 'image/jpeg') {
    ctx.fillStyle = '#fff';
    ctx.fillRect(0, 0, width, height);
  }
  ctx.drawImage(bitmap, 0, 0, width, height);
  const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, type, jpegQuality));
  if (!blob) throw new Error(`The browser could not encode the image as ${type}`);
  return blob;
}

export function toBase64(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => {
      const url = r.result as string;
      resolve(url.slice(url.indexOf(',') + 1));
    };
    r.onerror = () => reject(r.error ?? new Error('Could not read the image'));
    r.readAsDataURL(blob);
  });
}

export async function fetchImage(url: string): Promise<Blob> {
  const resp = await fetch(url);
  if (!resp.ok) throw new Error(`${resp.status} fetching the image`);
  const blob = await resp.blob();
  if (!blob.type.startsWith('image/')) throw new Error(`The answer's image is ${blob.type || 'of no image type'}`);
  return blob;
}
