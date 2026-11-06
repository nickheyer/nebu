import type { Component } from 'svelte';
import { reason, rpc } from '#lib/api/transport.js';
import { bytes, enumLabel, num } from '#lib/format.js';
import { ArtifactRole, ModelKind, ModelKindSchema } from '#lib/proto/nebu/v1/model_pb.js';
import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
import type { StoredModel } from '#lib/proto/nebu/v1/store_pb.js';
import { toast } from '#lib/shell/notify.svelte.js';
import { followTask } from '#lib/state/tasks.svelte.js';
import CodeIcon from 'phosphor-svelte/lib/CodeIcon';
import CubeIcon from 'phosphor-svelte/lib/CubeIcon';
import FileIcon from 'phosphor-svelte/lib/FileIcon';
import FileTextIcon from 'phosphor-svelte/lib/FileTextIcon';
import TranslateIcon from 'phosphor-svelte/lib/TranslateIcon';

export type ModelRef = Pick<StoredModel, 'sourceId' | 'repo' | 'group'>;

// The path a stored model's panel opens at, the repository encoded as one segment
export function modelPath(m: ModelRef): string {
	return `/models/${encodeURIComponent(m.sourceId)}/${encodeURIComponent(m.repo)}${m.group ? `/${encodeURIComponent(m.group)}` : ''}`;
}

export function modelKeyOf(m: ModelRef): string {
	return `${m.sourceId}/${m.repo}${m.group ? `/${m.group}` : ''}`;
}

// The launch wizard opened on a model, with a runtime and context when a fit pill chose them
export function launchHref(m: ModelRef, runtimeId?: string, context?: number): string {
	const q = new URLSearchParams({ model: modelKeyOf(m) });
	if (runtimeId) q.set('runtime', runtimeId);
	if (context) q.set('context', String(context));
	return `/serve/launch?${q}`;
}

export function kindLabel(kind: ModelKind): string {
	return kind === ModelKind.UNSPECIFIED ? '' : enumLabel(ModelKindSchema, kind);
}

// A parameter count as 70B or 7.2B
export function paramCount(n: bigint | number | undefined): string {
	const v = Number(n ?? 0);
	if (v <= 0) return '';
	if (v >= 1e12) return `${(v / 1e12).toFixed(1)}T`;
	if (v >= 1e9) return `${v >= 1e10 ? Math.round(v / 1e9) : (v / 1e9).toFixed(1)}B`;
	if (v >= 1e6) return `${Math.round(v / 1e6)}M`;
	return `${Math.round(v / 1e3)}K`;
}

// A context length in tokens as 8K or 128K when it is a whole number of thousands
export function contextLabel(n: number): string {
	if (n >= 1024 && n % 1024 === 0) return `${n / 1024}K`;
	return num(n);
}

export function sameModel(a: ModelRef, b: ModelRef): boolean {
	return a.sourceId === b.sourceId && a.repo === b.repo && a.group === b.group;
}

export function quality(level: number): string {
	return ['unknown', 'lowest', 'low', 'medium', 'high', 'full'][level] ?? '';
}

// Names of the runtimes whose bits are set in a compatibility mask, in listing order
export function runtimeNames(mask: number, runtimes: RuntimeStatus[]): string[] {
	return runtimes.filter((r) => r.runtime && (mask & r.runtime.bit) !== 0).map((r) => r.runtime?.name || r.runtime?.id || '');
}

export function roleIcon(role: ArtifactRole): Component<{ size?: number }> {
	switch (role) {
		case ArtifactRole.WEIGHTS:
		case ArtifactRole.PROJECTOR:
		case ArtifactRole.DRAFT:
			return CubeIcon;
		case ArtifactRole.TOKENIZER:
			return TranslateIcon;
		case ArtifactRole.CODE:
			return CodeIcon;
		case ArtifactRole.CONFIG:
		case ArtifactRole.TEMPLATE:
		case ArtifactRole.INDEX:
			return FileTextIcon;
		default:
			return FileIcon;
	}
}

// Starts a rehash of the model's blobs and follows its task in a toast
export async function verifyModel(m: StoredModel): Promise<void> {
	try {
		const res = await rpc.store.verify({ sourceId: m.sourceId, repo: m.repo, group: m.group });
		followTask(res.task, `Verify ${m.repo}`);
	} catch (err) {
		toast.error('Verify failed', reason(err));
	}
}

// Starts a copy of the model into a mirror layout under dir, answering whether the task started
export async function exportModel(m: StoredModel, dir: string): Promise<boolean> {
	try {
		const res = await rpc.store.export({ sourceId: m.sourceId, repo: m.repo, group: m.group, dir });
		followTask(res.task, `Export ${m.repo}`);
		return true;
	} catch (err) {
		toast.error('Export failed', reason(err));
		return false;
	}
}

// Removes the model and toasts what a collection freed, answering whether it was removed
export async function removeModel(m: StoredModel, gc: boolean): Promise<boolean> {
	try {
		const res = await rpc.store.removeModel({ sourceId: m.sourceId, repo: m.repo, group: m.group, gc });
		toast.success(`Removed ${m.repo}`, res.gc ? `${num(res.gc.removed)} blobs, ${bytes(res.gc.freedBytes)} freed` : undefined);
		return true;
	} catch (err) {
		toast.error('Remove failed', reason(err));
		return false;
	}
}
