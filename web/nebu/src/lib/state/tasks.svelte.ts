import { goto } from '$app/navigation';
import { isCancel, reason, rpc } from '#lib/api/transport.js';
import { notify, toast } from '#lib/shell/notify.svelte.js';
import { percent } from '#lib/format.js';
import { TaskState, type Task } from '#lib/proto/nebu/v1/task_pb.js';

// Raises a toast following the task until it ends or the toast is closed
export function followTask(task: Task | undefined, title?: string): void {
	if (!task) return;
	const item = notify({
		type: 'info',
		title: title ?? task.title,
		content: task.progress?.message || task.kind,
		progress: true,
		loading: true,
		close: true,
		duration: 0,
		progressProps: { value: percent(task.progress?.done, task.progress?.total) ?? 0, indeterminate: !task.progress?.total },
		actionProps: {
			label: 'Tasks',
			text: true,
			onclick: () => {
				void goto(`/tasks/${task.id}`);
				return 'dismissed';
			}
		}
	});
	const watch = new AbortController();
	let closed = false;
	void item.promise.then(() => {
		closed = true;
		watch.abort();
	});
	void (async () => {
		try {
			for await (const res of rpc.tasks.watchTask({ id: task.id }, { signal: watch.signal })) {
				const t = res.task;
				if (closed) return;
				if (!t) continue;
				const done = t.state === TaskState.SUCCEEDED || t.state === TaskState.FAILED || t.state === TaskState.CANCELED;
				item.update({
					type: t.state === TaskState.FAILED ? 'error' : t.state === TaskState.SUCCEEDED ? 'success' : 'info',
					title: t.title,
					content: t.state === TaskState.FAILED ? t.error : t.progress?.message || t.kind,
					loading: !done,
					progress: !done,
					progressProps: { value: done ? 100 : (percent(t.progress?.done, t.progress?.total) ?? 0), indeterminate: !done && !t.progress?.total }
				});
				if (t.state === TaskState.SUCCEEDED) item.restart({ duration: 4000 });
				if (t.state === TaskState.CANCELED) item.restart({ duration: 4000 });
				if (done) return;
			}
		} catch (err) {
			if (!closed && !isCancel(err)) toast.error('Task watch failed', reason(err));
		}
	})();
}
