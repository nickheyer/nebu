import type { Timestamp } from '@bufbuild/protobuf/wkt';
import { api } from './api';
import { confirm } from './confirm.svelte';
import { fail, ok } from './toast.svelte';
import { live, startedTask, taskActive } from './state.svelte';
import { newestFirst } from './format';
import { humanize } from './catalog';
import { ApiFlavor, InstallKind, type Install, type Runtime } from '$proto/runtime_pb';
import { SandboxKind, type Build } from '$proto/recipe_pb';
import type { Task } from '$proto/task_pb';

export const apiName: Record<ApiFlavor, string> = { [ApiFlavor.UNSPECIFIED]: '–', [ApiFlavor.OPENAI]: 'OpenAI', [ApiFlavor.ANTHROPIC]: 'Anthropic', [ApiFlavor.OLLAMA]: 'Ollama' };
export const verb: Record<InstallKind, string> = { [InstallKind.UNSPECIFIED]: 'Install', [InstallKind.ADOPTED]: 'Adopt', [InstallKind.PREBUILT]: 'Download', [InstallKind.BUILT]: 'Build' };
export const sandboxName: Record<SandboxKind, string> = { [SandboxKind.UNSPECIFIED]: '–', [SandboxKind.HOST]: 'Host', [SandboxKind.OCI]: 'Container' };

const byCreated = newestFirst<{ createdAt?: Timestamp }>((x) => x.createdAt);

export function buildsOf(runtimeId: string): Build[] {
  return [...live.builds.values()].filter((b) => b.runtimeId === runtimeId).sort(byCreated);
}

export function runtimeTasks(runtimeId: string): Task[] {
  return [...live.tasks.values()].filter((t) => taskActive(t) && t.labels.runtime === runtimeId && (t.kind === 'install' || t.kind === 'build')).sort(byCreated);
}

export function factItems(facts: Record<string, string>): [string, string][] {
  return Object.entries(facts)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => [humanize(k.replace(/\./g, ' ')), v]);
}

export async function install(runtime: Runtime, method: string, settings: Record<string, string>) {
  const r = await api.runtimes.install({ runtimeId: runtime.id, method, settings });
  startedTask(`Installing ${runtime.name}`, `Installed ${runtime.name}`, undefined, r.task);
}

async function remove(title: string, message: string, run: () => Promise<unknown>): Promise<boolean> {
  if (!(await confirm({ title: `Remove ${title}?`, message, action: 'Remove', tone: 'bad' }))) return false;
  try {
    await run();
    ok(`Removed ${title}`);
    return true;
  } catch (err) {
    fail(err, 'Remove failed');
    return false;
  }
}

export const removeInstall = (i: Install) => remove(i.version || i.id, i.path, () => api.runtimes.removeInstall({ id: i.id }));
export const removeBuild = (b: Build) => remove(`build ${b.variant || b.id}`, b.dir, () => api.builds.removeBuild({ id: b.id }));
