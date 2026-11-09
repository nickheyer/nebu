<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { live } from '#lib/live/events.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import Tip from '#lib/ui/Tip.svelte';
	import DownloadSimpleIcon from 'phosphor-svelte/lib/DownloadSimpleIcon';

	// Pulls named files of a repository onto the node, busy while the request or its task runs
	let { sourceId, repo, revision, paths, name, nodeId = '' }: { sourceId: string; repo: string; revision: string; paths: string[]; name: string; nodeId?: string } = $props();

	let busy = $state(false);
	let taskId = $state('');
	const nodeName = $derived(nodeId ? (live.nodes.get(nodeId)?.name ?? nodeId) : 'this node');
	// The task this button started, or any single file pull of one of its paths
	const running = $derived(live.activeTasks.some((t) => t.id === taskId || (t.labels['repo'] === repo && paths.includes(t.labels['path'] ?? ''))));

	async function pull() {
		busy = true;
		try {
			const res = await rpc.store.pull({ sourceId, repo, revision, paths, nodeId });
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
