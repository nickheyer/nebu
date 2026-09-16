import { InstallKind, ParamType, type Param, type RuntimeStatus } from '$proto/runtime_pb';
import { SandboxKind, type Build } from '$proto/recipe_pb';
import type { Task } from '$proto/task_pb';
import { newestFirst, type Tone } from './format';
import { live, taskFor } from './state.svelte';

// The verb an install method goes by, from what it does: adopt a binary, download a release, build a recipe
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

// The verb while the method runs
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

// How an install was obtained, in one word
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

// Where build steps run, in a word
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

// Whether this host can run a runtime, as one state: compatible, or what it needs
export function fit(status: RuntimeStatus): { tone: Tone; label: string } {
  if (status.compatible) return { tone: 'ok', label: 'Compatible' };
  return { tone: 'bad', label: `Needs ${status.unmet.join(', ')}` };
}

// The newest install task of a runtime, whatever state it is in
export function installTask(runtimeId: string): Task | undefined {
  return taskFor('install', { runtime: runtimeId }, false);
}

// What an install task is doing, by the verb of the method it runs
export function taskDoing(status: RuntimeStatus | undefined, task: Task | undefined): string {
  return methodDoing(status?.installs.find((o) => o.method?.id === task?.labels.method)?.method?.kind ?? InstallKind.UNSPECIFIED);
}

// Builds of one runtime, newest first
export function buildsOf(runtimeId: string): Build[] {
  return [...live.builds.values()].filter((b) => b.runtimeId === runtimeId).sort(newestFirst((b) => b.createdAt));
}

// Params by group in the order the runtime first names each group, ungrouped params first
export function paramGroups(params: Param[]): { name: string; params: Param[] }[] {
  const out: { name: string; params: Param[] }[] = [];
  for (const p of params) {
    let g = out.find((x) => x.name === p.group);
    if (!g) out.push((g = { name: p.group, params: [] }));
    g.params.push(p);
  }
  return out.sort((a, b) => Number(!!a.name) - Number(!!b.name));
}

// What a param accepts, in words: its choices, its bounds with their unit and step, on or off, or text
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

// What a param takes when nothing is set: auto for a solved param, else its default, on or off for a switch
export function defaultText(p: Param): string {
  if (p.solved) return 'auto';
  if (p.type === ParamType.BOOL) return p.default === 'true' ? 'on' : p.default === 'false' ? 'off' : p.default;
  return p.default;
}

