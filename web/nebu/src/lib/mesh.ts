import { Shape, PlanProfile, LinkClass, FitVerdict } from '$proto/estimate_pb';
import { NodeState, FormationState, AdmissionSide, AdmissionState, type Node, type Link, type Candidate, type Admission } from '$proto/mesh_pb';
import type { Tone } from './format';

export const shapeItems: { value: Shape; id: string; label: string; detail: string }[] = [
  { value: Shape.UNSPECIFIED, id: 'auto', label: 'Auto', detail: 'Choose a distribution that fits the selected nodes.' },
  { value: Shape.SOLO, id: 'solo', label: 'Solo', detail: 'Run the model on one node.' },
  { value: Shape.CHAIN, id: 'chain', label: 'Chain', detail: 'Split model layers across nodes.' },
  { value: Shape.LOCKSTEP, id: 'lockstep', label: 'Lockstep', detail: 'Split each layer across matching nodes with a fast interconnect.' },
  { value: Shape.RELAY, id: 'relay', label: 'Relay', detail: 'Process prompts on one node and generate tokens on another.' },
  { value: Shape.REPLICAS, id: 'replicas', label: 'Replicas', detail: 'Run a full copy of the model on each node.' },
  { value: Shape.DRAFT, id: 'draft', label: 'Draft', detail: 'Use a draft model on another node to propose tokens.' },
  { value: Shape.STAGES, id: 'stages', label: 'Stages', detail: 'Run image generation stages on separate nodes.' }
];

export const profileItems: { value: PlanProfile; id: string; label: string; detail: string }[] = [
  { value: PlanProfile.CHAT, id: 'chat', label: 'Chat', detail: 'Short prompts and responses.' },
  { value: PlanProfile.AGENT, id: 'agent', label: 'Agent', detail: 'Long prompts and short responses.' },
  { value: PlanProfile.BATCH, id: 'batch', label: 'Batch', detail: 'Many concurrent requests.' },
  { value: PlanProfile.AUTO, id: 'auto', label: 'Auto', detail: 'Use recent requests to this route.' }
];

export function shapeLabel(s: Shape | undefined): string {
  return shapeItems.find((x) => x.value === s)?.label ?? 'Auto';
}

export function seatLabel(role: string, rank: number): string {
  const label = role === 'head' ? 'Primary' : role ? role[0].toUpperCase() + role.slice(1) : 'Worker';
  return `${label} ${rank}`;
}

export function shapeOf(id: string): Shape {
  return shapeItems.find((x) => x.id === id)?.value ?? Shape.UNSPECIFIED;
}

export function shapeId(s: Shape | undefined): string {
  return shapeItems.find((x) => x.value === s)?.id ?? 'auto';
}

export function profileOf(id: string): PlanProfile {
  return profileItems.find((x) => x.id === id)?.value ?? PlanProfile.CHAT;
}

export function profileId(p: PlanProfile | undefined): string {
  return profileItems.find((x) => x.value === p)?.id ?? 'chat';
}

export function classLabel(c: LinkClass | undefined): string {
  switch (c) {
    case LinkClass.FABRIC:
      return 'fabric';
    case LinkClass.FAST:
      return 'fast';
    case LinkClass.LAN:
      return 'lan';
    case LinkClass.SLOW:
      return 'slow';
  }
  return 'unmeasured';
}

export function classTone(c: LinkClass | undefined): Tone {
  switch (c) {
    case LinkClass.FABRIC:
      return 'accent';
    case LinkClass.FAST:
      return 'ok';
    case LinkClass.LAN:
      return 'info';
    case LinkClass.SLOW:
      return 'warn';
  }
  return 'neutral';
}

export function verdictTone(v: FitVerdict | undefined): Tone {
  switch (v) {
    case FitVerdict.FITS:
      return 'ok';
    case FitVerdict.PARTIAL:
      return 'warn';
    case FitVerdict.NO:
      return 'bad';
  }
  return 'neutral';
}

export function verdictLabel(v: FitVerdict | undefined): string {
  switch (v) {
    case FitVerdict.FITS:
      return 'fits';
    case FitVerdict.PARTIAL:
      return 'partial';
    case FitVerdict.NO:
      return 'does not fit';
  }
  return '–';
}

export function nodeStateLabel(n: Node): string {
  if (n.self) return 'this node';
  switch (n.state) {
    case NodeState.READY:
      return 'ready';
    case NodeState.UNREACHABLE:
      return 'unreachable';
    case NodeState.GONE:
      return 'gone';
  }
  return 'contacting';
}

export function nodeTone(n: Node): Tone {
  if (n.self || n.state === NodeState.READY) return 'ok';
  if (n.state === NodeState.UNREACHABLE) return 'warn';
  if (n.state === NodeState.GONE) return 'bad';
  return 'neutral';
}

export function formationActive(state: FormationState): boolean {
  return state === FormationState.STARTING || state === FormationState.READY || state === FormationState.DEGRADED || state === FormationState.STOPPING;
}

// Bits per second as a rate a person reads
export function gbits(bps: bigint | number | undefined): string {
  const n = Number(bps ?? 0) * 8;
  if (n <= 0) return '–';
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)} Gb/s`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(0)} Mb/s`;
  return `${(n / 1e3).toFixed(0)} kb/s`;
}

export function gbytes(bps: number | undefined): string {
  const n = bps ?? 0;
  if (n <= 0) return '–';
  if (n >= 1e12) return `${(n / 1e12).toFixed(1)} TB/s`;
  return `${(n / 1e9).toFixed(0)} GB/s`;
}

export function tflops(flops: number | undefined): string {
  const n = flops ?? 0;
  if (n <= 0) return '–';
  return `${(n / 1e12).toFixed(0)} TFLOPS`;
}

export function micros(us: number | undefined): string {
  const n = us ?? 0;
  if (n <= 0) return '–';
  if (n >= 1000) return `${(n / 1000).toFixed(1)} ms`;
  return `${n} µs`;
}

export function seconds(s: number | undefined): string {
  const n = s ?? 0;
  if (n <= 0) return '–';
  if (n >= 1) return `${n.toFixed(2)} s`;
  if (n >= 1e-3) return `${(n * 1e3).toFixed(0)} ms`;
  return `${(n * 1e6).toFixed(0)} µs`;
}

export function tps(t: number | undefined): string {
  const n = t ?? 0;
  return n > 0 ? n.toFixed(1) : '–';
}

export function speedup(x: number | undefined): string {
  const n = x ?? 0;
  return n > 0 ? `${n.toFixed(2)}×` : '–';
}

// A link between two nodes in either direction, the one measured from the first when both exist
export function linkBetween(links: Link[], from: string, to: string): Link | undefined {
  return links.find((l) => l.from === from && l.to === to) ?? links.find((l) => l.from === to && l.to === from);
}

// Whether a candidate is the chosen one
export function chosen(c: Candidate, shape: Shape, head: string): boolean {
  return c.shape === shape && c.verdict === FitVerdict.FITS && (!head || c.head === head);
}

// Where an admission stands, as the page says it
export function admissionLabel(a: Admission): string {
  switch (a.state) {
    case AdmissionState.PENDING:
      return a.side === AdmissionSide.MEMBER ? (a.asked ? 'needs approval' : 'invitation sent') : a.asked ? 'awaiting approval' : 'invited';
    case AdmissionState.DENIED:
      return 'denied';
    case AdmissionState.DECLINED:
      return 'declined';
    case AdmissionState.EXPIRED:
      return 'expired';
    case AdmissionState.FAILED:
      return 'failed';
    case AdmissionState.ADMITTED:
      return 'joining';
    case AdmissionState.JOINED:
      return 'joined';
    case AdmissionState.DISMISSED:
      return 'dismissed';
  }
  return '–';
}

export function admissionTone(s: AdmissionState): Tone {
  switch (s) {
    case AdmissionState.PENDING:
      return 'info';
    case AdmissionState.ADMITTED:
      return 'accent';
    case AdmissionState.JOINED:
      return 'ok';
    case AdmissionState.DENIED:
    case AdmissionState.DECLINED:
    case AdmissionState.FAILED:
      return 'bad';
    case AdmissionState.EXPIRED:
      return 'warn';
  }
  return 'neutral';
}

// How the node requested membership.
export function admissionWhat(a: Admission): string {
  if (a.invited && a.asked) return 'Invitation accepted';
  if (a.invited) return 'Invitation';
  return 'Join request';
}
