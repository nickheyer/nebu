import { InstallKind, ParamType, type Param, type RuntimeStatus } from '$proto/runtime_pb';
import { ModelKind } from '$proto/model_pb';
import { SandboxKind, type Build } from '$proto/recipe_pb';
import type { Task } from '$proto/task_pb';
import { newestFirst, type Tone } from './format';
import { live, taskFor } from './state.svelte';

export function methodVerb(kind: InstallKind): string {
  switch (kind) {
    case InstallKind.ADOPTED:
      return 'Adopt';
    case InstallKind.PREBUILT:
      return 'Download';
    case InstallKind.BUILT:
      return 'Build';
    default:
      return 'Install';
  }
}

export function methodDoing(kind: InstallKind): string {
  switch (kind) {
    case InstallKind.ADOPTED:
      return 'Adopting';
    case InstallKind.PREBUILT:
      return 'Downloading';
    case InstallKind.BUILT:
      return 'Building';
    default:
      return 'Installing';
  }
}

export function kindWord(kind: InstallKind): string {
  switch (kind) {
    case InstallKind.ADOPTED:
      return 'adopted';
    case InstallKind.PREBUILT:
      return 'downloaded';
    case InstallKind.BUILT:
      return 'built';
    default:
      return '';
  }
}

export function sandboxWord(kind: SandboxKind): string {
  switch (kind) {
    case SandboxKind.HOST:
      return 'host';
    case SandboxKind.OCI:
      return 'container';
    default:
      return '';
  }
}

export function fit(status: RuntimeStatus): { tone: Tone; label: string } {
  if (status.compatible) return { tone: 'ok', label: 'Compatible' };
  return { tone: 'bad', label: `Needs ${status.unmet.join(', ')}` };
}

// Include completed install tasks.
export function installTask(runtimeId: string): Task | undefined {
  return taskFor('install', { runtime: runtimeId }, false);
}

export function taskDoing(status: RuntimeStatus | undefined, task: Task | undefined): string {
  return methodDoing(status?.installs.find((o) => o.method?.id === task?.labels.method)?.method?.kind ?? InstallKind.UNSPECIFIED);
}

export function buildsOf(runtimeId: string): Build[] {
  return [...live.builds.values()].filter((b) => b.runtimeId === runtimeId).sort(newestFirst((b) => b.createdAt));
}

// Preserve runtime group order, with ungrouped params first.
export function paramGroups(params: Param[]): { name: string; params: Param[] }[] {
  const out: { name: string; params: Param[] }[] = [];
  for (const p of params) {
    let g = out.find((x) => x.name === p.group);
    if (!g) out.push((g = { name: p.group, params: [] }));
    g.params.push(p);
  }
  return out.sort((a, b) => Number(!!a.name) - Number(!!b.name));
}

// Each parallel sequence gets n_ctx / n_parallel of the pool unless kv_unified shares the whole pool, as llama.cpp divides it
export function sequenceContext(params: Record<string, string> | undefined): { total: number; perSequence: number; sequences: number } {
  const total = Number(params?.['n_ctx'] ?? 0);
  if (!Number.isFinite(total) || total <= 0) return { total: 0, perSequence: 0, sequences: 1 };
  const parallel = Math.max(1, Math.floor(Number(params?.['n_parallel'] ?? 1) || 1));
  const unified = /^(true|on|1)$/i.test(params?.['kv_unified'] ?? '');
  const sequences = unified ? 1 : parallel;
  return { total, perSequence: unified ? total : Math.ceil(total / parallel / 256) * 256, sequences };
}

// A context length for display, each sequence's share with the pool and sequence count behind it
export function contextText(params: Record<string, string> | undefined, format: (n: number) => string): { text: string; title: string } {
  const c = sequenceContext(params);
  if (!c.total) return { text: '', title: '' };
  if (c.sequences === 1) return { text: format(c.perSequence), title: `${c.total.toLocaleString()} tokens` };
  return { text: `${format(c.perSequence)} × ${c.sequences}`, title: `${c.perSequence.toLocaleString()} tokens for each of ${c.sequences} parallel sequences, ${c.total.toLocaleString()} in the pool` };
}

export function accepts(p: Param): string {
  if (p.choices.length) return p.choices.filter(Boolean).join(', ');
  switch (p.type) {
    case ParamType.BOOL:
      return 'on or off';
    case ParamType.INT:
    case ParamType.FLOAT: {
      let range = '';
      if (p.max !== 0) range = `${p.min}–${p.max}`;
      else if (p.min !== 0) range = `≥ ${p.min}`;
      let text = range ? (p.unit ? `${range} ${p.unit}` : range) : p.unit ? `any number of ${p.unit}` : 'any number';
      if (p.step && !(p.type === ParamType.INT && p.step === 1)) text += `, step ${p.step}`;
      return text;
    }
    default:
      return 'text';
  }
}

export function defaultText(p: Param): string {
  if (p.solved) return 'auto';
  if (p.type === ParamType.BOOL) return p.default === 'true' ? 'on' : p.default === 'false' ? 'off' : p.default;
  return p.default;
}


export function servesWord(kind: ModelKind): string {
  return kind === ModelKind.DIFFUSION ? 'image and video models' : 'language models';
}

// Use the daemon's runtime bit assignments and order.
export function runtimesOf(mask: number, runtimes: RuntimeStatus[]): RuntimeStatus[] {
  return runtimes.filter((r) => r.runtime && r.runtime.bit !== 0 && (mask & r.runtime.bit) !== 0);
}

export function runsOn(mask: number, r: RuntimeStatus | undefined): boolean {
  return !!r?.runtime && r.runtime.bit !== 0 && (mask & r.runtime.bit) !== 0;
}
