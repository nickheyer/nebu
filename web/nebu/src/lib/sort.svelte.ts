// Column sort state a table shares with its headers
export class TableSort {
  key = $state('');
  dir = $state<'asc' | 'desc'>('desc');

  constructor(key: string, dir: 'asc' | 'desc' = 'desc') {
    this.key = key;
    this.dir = dir;
  }

  // Clicking the active column flips it, another column starts descending
  toggle(key: string) {
    if (this.key === key) this.dir = this.dir === 'asc' ? 'desc' : 'asc';
    else {
      this.key = key;
      this.dir = 'desc';
    }
  }

  // Sorts a copy by the value a column produces, numbers, bigints, strings, or dates
  apply<T>(rows: T[], value: (row: T, key: string) => string | number | bigint | undefined): T[] {
    const sign = this.dir === 'asc' ? 1 : -1;
    return [...rows].sort((a, b) => {
      const x = value(a, this.key);
      const y = value(b, this.key);
      if (x === undefined || x === null) return 1;
      if (y === undefined || y === null) return -1;
      if (typeof x === 'string' || typeof y === 'string') return String(x).localeCompare(String(y)) * sign;
      return (x < y ? -1 : x > y ? 1 : 0) * sign;
    });
  }
}
