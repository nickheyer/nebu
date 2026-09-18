// Browser storage that may be missing or refused, every read falling back to empty
export function readLocal(key: string): string {
  try {
    return localStorage.getItem(key) ?? '';
  } catch {
    return '';
  }
}

// Writes a value, an empty one removing the key
export function writeLocal(key: string, value: string) {
  try {
    if (value) localStorage.setItem(key, value);
    else localStorage.removeItem(key);
  } catch {
    // private windows and locked down browsers refuse storage
  }
}

// The stored keys under a prefix, none where storage is refused
export function localKeys(prefix: string): string[] {
  try {
    const out: string[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (k?.startsWith(prefix)) out.push(k);
    }
    return out;
  } catch {
    return [];
  }
}
