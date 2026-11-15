import { isCancel, rpc } from '#lib/api/transport.js';
import { bytes, num, pct, percent } from '#lib/format.js';
import type { Task, TaskProgress } from '#lib/proto/nebu/v1/task_pb.js';
import { ProgressUnit, TaskState } from '#lib/proto/nebu/v1/task_pb.js';
import type { Fact } from '#lib/ui/Tip.svelte';

export const taskKinds = ['pull', 'run', 'swap', 'install', 'build', 'verify', 'export', 'probe', 'formation'];

export function active(t: Task): boolean {
	return t.state === TaskState.PENDING || t.state === TaskState.RUNNING;
}

// The page of what a task made or changed, from the label naming it, as the words of the button that opens it
export function madeLink(t: Task): { label: string; href: string } | undefined {
	const l = t.labels;
	if (l['instance']) return { label: 'Open the instance', href: `/serve/${l['instance']}` };
	if (l['formation']) return { label: 'Open the formation', href: `/mesh/formations/${encodeURIComponent(l['formation'])}` };
	if (l['slot']) return { label: 'Open the slot', href: `/serve?slot=${encodeURIComponent(l['slot'])}` };
	if (l['build'] && l['runtime']) return { label: 'Open the build', href: `/runtimes/${encodeURIComponent(l['runtime'])}/builds` };
	if (l['runtime']) return { label: 'Open the runtime', href: `/runtimes/${encodeURIComponent(l['runtime'])}` };
	if (l['repo']) return { label: 'Open the model', href: `/models?query=${encodeURIComponent(l['repo'])}` };
	if (t.kind === 'probe') return { label: 'Open what it found', href: '/' };
	return undefined;
}

// Where a run of a task stands among the runs folded into one row with it, newest first, with the runs either side
export type RunPlace = { at: number; of: number; newer?: string; older?: string };

// The percent a progress reached as a label, a dash while its total is unknown
export function progressLabel(p: TaskProgress | undefined): string {
	const v = percent(p?.done, p?.total);
	return v === undefined ? '—' : pct(v);
}

// Done against the total in the unit the progress counts, done alone while the total is unknown
export function progressAmount(p: TaskProgress | undefined): string {
	if (!p || (p.done === 0n && p.total === 0n)) return '';
	const show = p.unit === ProgressUnit.BYTES ? bytes : num;
	return p.total ? `${show(p.done)} of ${show(p.total)}` : show(p.done);
}

// The message and amount of a progress for its tooltip
export function progressFacts(p: TaskProgress | undefined): Fact[] {
	return [
		{ label: 'message', value: p?.message ?? '' },
		{ label: p?.unit === ProgressUnit.BYTES ? 'bytes' : 'steps', value: progressAmount(p) }
	].filter((f) => f.value !== '');
}

// Opens a task log for LogView, following the task while it runs and reading the stored tail once it ended
export function taskLogs(id: string, running: boolean) {
	return (tail: number, follow: boolean, signal: AbortSignal): AsyncIterable<string[]> =>
		(async function* () {
			if (running && follow) {
				try {
					for await (const res of rpc.tasks.watchTask({ id }, { signal })) if (res.logs.length) yield res.logs;
				} catch (err) {
					if (!isCancel(err)) throw err;
				}
				return;
			}
			const res = await rpc.tasks.getTask({ id }, { signal });
			yield res.logs.slice(-tail);
		})();
}
