import { isCancel, reason, rpc } from '#lib/api/transport.js';
import { NeoErrorNotificationNotFound } from '#lib/neo/utils/error.utils.js';
import { notify, toast } from '#lib/shell/notify.svelte.js';
import { percent } from '#lib/format.js';
import { TaskState, type Task } from '#lib/proto/nebu/v1/task_pb.js';
import AppWindowIcon from 'phosphor-svelte/lib/AppWindowIcon';

// Raises a toast following the task until it ends or the toast is gone
export function followTask(task: Task | undefined, title?: string): void {
	if (!task) return;
	const item = notify({
		type: 'info',
		title: title ?? task.title,
		content: task.progress?.message || undefined,
		link: { label: 'View in tasks', href: `/tasks/${task.id}`, icon: AppWindowIcon },
		progress: true,
		loading: true,
		close: true,
		duration: 0,
		progressProps: { value: percent(task.progress?.done, task.progress?.total) ?? 0, indeterminate: !task.progress?.total }
	});
	const watch = new AbortController();
	let gone = false;
	const leave = () => {
		gone = true;
		watch.abort();
	};
	void item.promise.then(leave);

	// Changes the toast, answering false once the stack no longer holds it
	const put = (change: () => void): boolean => {
		if (gone) return false;
		try {
			change();
			return true;
		} catch (err) {
			if (!(err instanceof NeoErrorNotificationNotFound)) throw err;
			leave();
			return false;
		}
	};

	void (async () => {
		try {
			for await (const res of rpc.tasks.watchTask({ id: task.id }, { signal: watch.signal })) {
				const t = res.task;
				if (gone) return;
				if (!t) continue;
				const done = t.state === TaskState.SUCCEEDED || t.state === TaskState.FAILED || t.state === TaskState.CANCELED;
				const shown = put(() => {
					item.update({
						type: t.state === TaskState.FAILED ? 'error' : t.state === TaskState.SUCCEEDED ? 'success' : 'info',
						title: title ?? t.title,
						content: (t.state === TaskState.FAILED ? t.error : t.progress?.message) || undefined,
						loading: !done,
						progress: !done,
						progressProps: { value: done ? 100 : (percent(t.progress?.done, t.progress?.total) ?? 0), indeterminate: !done && !t.progress?.total }
					});
					if (t.state === TaskState.SUCCEEDED || t.state === TaskState.CANCELED) item.restart({ duration: 4000 });
				});
				if (done || !shown) return;
			}
		} catch (err) {
			if (!gone && !isCancel(err)) toast.error('Task watch failed', reason(err));
		}
	})();
}
