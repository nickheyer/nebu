<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { live } from '#lib/live/events.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import Tip from '#lib/ui/Tip.svelte';
	import DownloadSimpleIcon from 'phosphor-svelte/lib/DownloadSimpleIcon';

	// Pulls one weight group with what it needs, or the named files, busy while its task runs
	let {
		sourceId,
		repo,
		revision,
		group,
		paths = [],
		name,
		nodeId = ''
	}: { sourceId: string; repo: string; revision: string; group?: string; paths?: string[]; name: string; nodeId?: string } = $props();

	let busy = $state(false);
	let taskId = $state('');
	const nodeName = $derived(nodeId ? (live.nodes.get(nodeId)?.name ?? nodeId) : 'this node');
	// The task this button started, or any pull of the same group or of one of the same files
	const running = $derived(
		live.activeTasks.some((t) => t.id === taskId || (t.kind === 'pull' && t.labels['repo'] === repo && (group === undefined ? paths.includes(t.labels['path'] ?? '') : !t.labels['path'] && t.labels['group'] === group)))
	);

	async function pull() {
		busy = true;
		try {
			const res = await rpc.store.pull(group === undefined ? { sourceId, repo, revision, paths, nodeId } : { sourceId, repo, revision, group, nodeId });
			taskId = res.task?.id ?? '';
			followTask(res.task, `Pull ${name}`);
		} catch (err) {
			toast.error('Pull failed', reason(err));
		} finally {
			busy = false;
		}
	}
</script>

<Tip text="Pull to {nodeName}" tag="span">
	<NeoButton text aria-label="Pull {name} to {nodeName}" loading={busy || running} onclick={pull}>{#snippet icon()}<DownloadSimpleIcon size={18} />{/snippet}</NeoButton>
</Tip>
