<script lang="ts" module>
  import type { Tone } from '$lib/format';

  export interface SizeItem {
    label?: string;
    size: bigint | number;
    tone?: Tone;
  }

  // Later layers draw over earlier layers.
  export interface Overlay {
    start?: 'left' | 'right';
    items: SizeItem[];
    markers?: boolean;
  }

  // Binary units for memory, decimal for storage.
  export type Units = 'binary' | 'decimal';
</script>

<script lang="ts">
  import { bytes, ratioBytes, ratioStorage, storage } from '$lib/format';

  let {
    total,
    overlays,
    units = 'binary',
    figure,
    free,
    dense = false,
    class: cls = ''
  }: {
    total: bigint | number;
    overlays: Overlay[];
    units?: Units;
    figure?: 'left' | 'right';
    free?: string;
    dense?: boolean;
    class?: string;
  } = $props();

  const fills: Record<Tone, string> = { ok: 'bg-ok', warn: 'bg-warn', bad: 'bg-bad', info: 'bg-info', accent: 'bg-accent', neutral: 'bg-fg-faint' };
  const dots: Record<Tone, string> = { ok: 'text-ok', warn: 'text-warn', bad: 'text-bad', info: 'text-info', accent: 'text-accent', neutral: 'text-fg-faint' };
  const palette: Tone[] = ['accent', 'info', 'warn', 'ok', 'neutral'];

  const format = $derived(units === 'decimal' ? storage : bytes);
  const ratio = $derived(units === 'decimal' ? ratioStorage : ratioBytes);

  interface Segment {
    left: number;
    width: number;
    tone: Tone;
    text: string;
  }

  interface Marker {
    at: number;
    text: string;
    tone: Tone;
    // Filled markers show usage. Hollow markers show free space.
    hollow: boolean;
    side: 'left' | 'right';
  }

  const n = (v: bigint | number) => Math.max(Number(v), 0);
  const totalN = $derived(n(total));
  const extent = (o: Overlay) => o.items.reduce((a, i) => a + n(i.size), 0);
  const extents = $derived(overlays.map(extent));
  const used = $derived(Math.max(0, ...extents));
  // Expand the scale to include overflow.
  const scale = $derived(Math.max(totalN, used, 1));
  const over = $derived(used - totalN);
  const pct = (v: number) => (v / scale) * 100;

  // Split segments at capacity to mark overflow.
  function layer(o: Overlay, index: number): { segments: Segment[]; markers: Marker[] } {
    const segments: Segment[] = [];
    const markers: Marker[] = [];
    const fromRight = o.start === 'right';
    let offset = 0;
    o.items.forEach((item, i) => {
      const size = n(item.size);
      if (size <= 0) return;
      const tone = item.tone ?? palette[(index + i) % palette.length];
      const text = [item.label, format(item.size)].filter(Boolean).join(' ');
      const inside = Math.max(0, Math.min(size, totalN - offset));
      const outside = size - inside;
      const place = (from: number, len: number, t: Tone, title: string, mark?: string) => {
        const left = fromRight ? scale - from - len : from;
        segments.push({ left: pct(left), width: pct(len), tone: t, text: title });
        if (mark) markers.push({ at: pct(fromRight ? left : left + len), text: mark, tone: t, hollow: false, side: fromRight ? 'left' : 'right' });
      };
      if (inside > 0) place(offset, inside, tone, text, outside <= 0 && item.label ? text : undefined);
      if (outside > 0) place(offset + inside, outside, 'bad', `${format(over)} over`, `${format(over)} over`);
      offset += size;
    });
    return { segments, markers };
  }
  const layers = $derived(overlays.map((o, i) => ({ overlay: o, ...layer(o, i) })));

  // Free space lies between the left and right layers.
  const reach = (start: 'left' | 'right') => Math.max(0, ...overlays.filter((o) => (o.start ?? 'left') === start).map(extent));
  const remaining = $derived(totalN - reach('left') - reach('right'));
  const markers = $derived.by((): Marker[] => {
    const out = layers.filter((l) => l.overlay.markers !== false).flatMap((l) => l.markers);
    if (free && remaining > 0) out.push({ at: pct(scale - reach('right')), text: `${format(remaining)} ${free}`, tone: 'neutral', hollow: true, side: 'left' });
    return out.sort((a, b) => a.at - b.at);
  });

  // Measure markers to avoid overlapping labels.
  let width = $state(0);
  let widths = $state<number[]>([]);
  // Offset by 3px to center the 6px marker.
  const half = 3;
  const gap = 8;
  interface Spot {
    x0: number;
    x1: number;
    side: 'left' | 'right';
  }
  // Try the preferred side first.
  function spots(m: Marker, w: number): [Spot, Spot] {
    const x = (m.at / 100) * width;
    const right: Spot = { x0: x - half, x1: x - half + w, side: 'right' };
    const left: Spot = { x0: x + half - w, x1: x + half, side: 'left' };
    return m.side === 'right' ? [right, left] : [left, right];
  }
  const overlaps = (a: Spot, b: Spot) => a.x1 + gap > b.x0 && b.x1 + gap > a.x0;
  const clear = (s: Spot, taken: Spot[]) => s.x0 >= -half && s.x1 <= width + half && !taken.some((t) => overlaps(s, t));
  // Try the opposite side if a label overlaps or exceeds the bar.
  // Hide labels that fit on neither side, retaining the segment tooltip.
  const placed = $derived.by((): (Spot | undefined)[] => {
    const taken: Spot[] = [];
    return markers.map((m, i) => {
      const w = widths[i];
      if (!width || !w) return undefined;
      const [want, other] = spots(m, w);
      const next = markers[i + 1];
      const nextWant = next && widths[i + 1] ? spots(next, widths[i + 1])[0] : undefined;
      const pick = nextWant && overlaps(want, nextWant) && clear(other, taken) ? other : clear(want, taken) ? want : clear(other, taken) ? other : undefined;
      if (pick) taken.push(pick);
      return pick;
    });
  });
</script>

{#snippet fig()}
  <span class="flex w-28 shrink-0 items-center tabular-nums text-fg {dense ? 'h-2.5 text-[11px]' : 'h-4 text-xs'} {figure === 'left' ? 'justify-end' : 'justify-start'}" title="{format(used)} of {format(total)}">{ratio(used, total)}</span>
{/snippet}

<div class="flex min-w-0 items-start gap-3 {cls}">
  {#if figure === 'left'}{@render fig()}{/if}
  <div class="flex min-w-0 flex-1 flex-col gap-1">
    <div class="relative w-full overflow-hidden rounded-md bg-line/60 {dense ? 'h-2.5' : 'h-4'}" title="{format(total)} total">
      {#each layers as layer, li (li)}
        {#each layer.segments as s, si (si)}
          <div class="absolute inset-y-0 {fills[s.tone]} transition-[left,width] duration-500" style="left: {s.left}%; width: {s.width}%; z-index: {li + 1}" title={s.text}></div>
        {/each}
      {/each}
      {#if over > 0}
        <div class="absolute inset-y-0 w-px bg-fg" style="left: {pct(totalN)}%; z-index: {layers.length + 1}" title="{format(total)} total"></div>
      {/if}
    </div>
    <!-- Reserve marker height even when the bar is empty. -->
    {#if !dense}
      <div class="relative h-4 w-full text-xs leading-4 tabular-nums" bind:clientWidth={width}>
        {#each markers as m, i (i)}
          {@const p = placed[i]}
          <span
            class="absolute top-0 inline-flex items-center gap-1.5 whitespace-nowrap {p ? '' : 'invisible'} {p?.side === 'left' ? '-translate-x-full flex-row-reverse' : ''} {m.hollow ? 'text-fg-faint' : 'text-fg-muted'}"
            style="left: calc({m.at}% {p?.side === 'left' ? '+' : '-'} {half}px)"
            bind:clientWidth={widths[i]}
          >
            {#if m.hollow}
              <span class="h-1.5 w-1.5 shrink-0 rounded-full border-[1.5px] border-fg-muted"></span>
            {:else}
              <span class="dot {dots[m.tone]}"></span>
            {/if}
            <span>{m.text}</span>
          </span>
        {/each}
      </div>
    {/if}
  </div>
  {#if figure === 'right'}{@render fig()}{/if}
</div>
