import type { Attachment } from 'svelte/attachments';

export const closed = 'M4 12c1.38 -0.77 4.42 -1.3 8 -1.3c3.58 0 6.62 0.53 8 1.3c-1.38 0.77 -4.42 1.3 -8 1.3c-3.58 0 -6.62 -0.53 -8 -1.3Z';
export const open = 'M2 12c1.72 -3.83 5.53 -6.5 10 -6.5c4.47 0 8.28 2.67 10 6.5c-1.72 3.83 -5.53 6.5 -10 6.5c-4.47 0 -8.28 -2.67 -10 -6.5Z';
export const radius = 3;

const box = 24;
const gap = 0.5;
const rate = 45;

type Plane = [nx: number, ny: number, room: number];

interface Eye {
  pupil: SVGCircleElement;
  icon: SVGSVGElement;
  inset: number;
  x: number;
  y: number;
  vx: number;
  vy: number;
  tx: number;
  ty: number;
  sx: number;
  sy: number;
}

const lens = bounds(open, 24);
const eyes = new Set<Eye>();
let calm: MediaQueryList | undefined;
let watching = false;
let seen = false;
let cursorX = 0;
let cursorY = 0;
let last = 0;
let frame = 0;

// Outward half planes of a convex path of relative cubics, measured from the icon center
function bounds(path: string, steps: number): Plane[] {
  const n = path.match(/-?[\d.]+/g)!.map(Number);
  const points: [number, number][] = [];
  let [x, y] = [n[0]!, n[1]!];
  for (let i = 2; i + 5 < n.length; i += 6) {
    const [x1, y1, x2, y2, x3, y3] = [x + n[i]!, y + n[i + 1]!, x + n[i + 2]!, y + n[i + 3]!, x + n[i + 4]!, y + n[i + 5]!];
    for (let s = 0; s < steps; s++) {
      const t = s / steps;
      const m = 1 - t;
      points.push([
        m * m * m * x + 3 * m * m * t * x1 + 3 * m * t * t * x2 + t * t * t * x3 - box / 2,
        m * m * m * y + 3 * m * m * t * y1 + 3 * m * t * t * y2 + t * t * t * y3 - box / 2,
      ]);
    }
    [x, y] = [x3, y3];
  }
  return points.map(([ax, ay], i): Plane => {
    const [bx, by] = points[(i + 1) % points.length]!;
    const length = Math.hypot(bx - ax, by - ay);
    const nx = (by - ay) / length;
    const ny = (ax - bx) / length;
    const room = nx * ax + ny * ay;
    return room < 0 ? [-nx, -ny, -room] : [nx, ny, room];
  });
}

// Stores where the mouse or pen cursor is
function note(event: MouseEvent) {
  if (event instanceof PointerEvent && event.pointerType === 'touch') return;
  cursorX = event.clientX;
  cursorY = event.clientY;
  seen = true;
}

// Starts recording the cursor position so an eye knows it before the cursor next moves
export function watchCursor() {
  if (watching) return;
  watching = true;
  for (const type of ['pointermove', 'pointerover', 'pointerdown', 'dragover'] as const) window.addEventListener(type, note, { capture: true, passive: true });
}

// Targets the cursor like an eyeball resting against the screen, keeping the pupil rim inside the lid stroke
function aim(eye: Eye) {
  const { left, top, width, height } = eye.icon.getBoundingClientRect();
  if (!seen || !width || !height) return;
  const dx = ((cursorX - left) / width - 0.5) * box;
  const dy = ((cursorY - top) / height - 0.5) * box;
  let strain = 0;
  for (const [nx, ny, room] of lens) strain = Math.max(strain, (nx * dx + ny * dy) / Math.max(room - eye.inset, Number.EPSILON));
  const ease = Math.hypot(1, strain);
  eye.tx = dx / ease;
  eye.ty = dy / ease;
}

// Moves the pupil element once its position differs visibly from where it was last put
function draw(eye: Eye) {
  if (Math.hypot(eye.x - eye.sx, eye.y - eye.sy) < 1e-3) return;
  eye.sx = eye.x;
  eye.sy = eye.y;
  eye.pupil.setAttribute('cx', `${eye.x}`);
  eye.pupil.setAttribute('cy', `${eye.y}`);
}

// Glides every pupil toward its target on a critically damped spring, or jumps there under reduced motion
function tick(now: number) {
  const dt = Math.min(Math.max(now - last, 0), 50) / 1000;
  const fade = (calm ??= matchMedia('(prefers-reduced-motion: reduce)')).matches ? 0 : Math.exp(-rate * dt);
  last = now;
  for (const eye of eyes) aim(eye);
  for (const eye of eyes) {
    const jx = (eye.vx + rate * (eye.x - eye.tx)) * dt;
    const jy = (eye.vy + rate * (eye.y - eye.ty)) * dt;
    eye.x = eye.tx + (eye.x - eye.tx + jx) * fade;
    eye.y = eye.ty + (eye.y - eye.ty + jy) * fade;
    eye.vx = (eye.vx - rate * jx) * fade;
    eye.vy = (eye.vy - rate * jy) * fade;
    draw(eye);
  }
  frame = requestAnimationFrame(tick);
}

// Keeps a pupil looking at the cursor from inside a lid drawn with the given stroke width
export function gaze(stroke: number): Attachment<SVGCircleElement> {
  return (pupil) => {
    const eye: Eye = { pupil, icon: pupil.ownerSVGElement!, inset: radius + stroke / 2 + gap, x: 0, y: 0, vx: 0, vy: 0, tx: 0, ty: 0, sx: 0, sy: 0 };
    watchCursor();
    aim(eye);
    eye.x = eye.tx;
    eye.y = eye.ty;
    draw(eye);
    if (!eyes.size) {
      last = performance.now();
      frame = requestAnimationFrame(tick);
    }
    eyes.add(eye);
    return () => {
      eyes.delete(eye);
      if (!eyes.size) cancelAnimationFrame(frame);
    };
  };
}
