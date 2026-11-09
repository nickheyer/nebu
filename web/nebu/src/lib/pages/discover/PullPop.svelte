<script lang="ts">
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes } from '#lib/format.js';
	import type { Part, PartsResponse } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { TaskState, type Task } from '#lib/proto/nebu/v1/task_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import Pop from '#lib/ui/Pop.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import { launchHref } from '#lib/pages/models/models.js';
	import PuzzlePieceIcon from 'phosphor-svelte/lib/PuzzlePieceIcon';

	// The pull of one weight group, its missing parts chosen alongside, from the row's own action
	let { sourceId, repo, revision, group, groupBytes, nodeId = '' }: { sourceId: string; repo: string; revision: string; group: string; groupBytes: number; nodeId?: string } = $props();

	let open = $state(false);
	let asked = false;
	let partsRes = $state<PartsResponse | undefined>(undefined);
	let partsError = $state('');
	let chosen = $state<string[]>([]);
	let pulling = $state(false);

	const ref = $derived({ sourceId, repo, group });
	const missing = $derived((partsRes?.parts ?? []).filter((p) => p.required && !p.stored && !p.bundled));
	const allChosen = $derived(missing.every((p) => chosen.includes(p.slot)));
	const partsBytes = $derived(allChosen ? Number(partsRes?.pullBytes ?? 0) : missing.filter((p) => chosen.includes(p.slot)).reduce((n, p) => n + Number(p.sizeBytes), 0));

	// Lists the parts the first time the popover opens
	$effect(() => {
		if (!open || asked) return;
		asked = true;
		rpc.estimate
			.parts({ sourceId, repo, revision, group })
			.then((res) => {
				partsRes = res;
				chosen = res.parts.filter((p) => p.required && !p.stored && !p.bundled && !p.error).map((p) => p.slot);
			})
			.catch((err) => (partsError = reason(err)));
	});

	async function succeeded(task: Task): Promise<boolean> {
		for await (const w of rpc.tasks.watchTask({ id: task.id })) {
			const s = w.task?.state;
			if (s === TaskState.SUCCEEDED) return true;
			if (s === TaskState.FAILED || s === TaskState.CANCELED) return false;
		}
		throw new Error(`${task.title}: watch ended early`);
	}

	// Pulls the group with its chosen parts, launching after when asked
	async function pull(launch: boolean) {
		pulling = true;
		const own = allChosen;
		try {
			const started: Task[] = [];
			const main = await rpc.store.pull({ sourceId, repo, revision, group, alone: !own, nodeId });
			if (main.task) {
				followTask(main.task, `Pull ${repo} ${group}`.trim());
				started.push(main.task);
			}
			if (!own) {
				for (const p of missing.filter((x: Part) => chosen.includes(x.slot))) {
					const res = await rpc.store.pull({ sourceId: p.sourceId, repo: p.repo, revision: p.revision, group: p.group, alone: true, nodeId, paths: p.paths, slot: p.paths.length ? p.slot : '' });
					if (res.task) {
						followTask(res.task, `Pull ${p.label || p.slot}`);
						started.push(res.task);
					}
				}
			}
			open = false;
			if (launch && (await Promise.all(started.map(succeeded))).every(Boolean)) void goto(launchHref(ref));
		} catch (err) {
			toast.error('Pull failed', reason(err));
		} finally {
			pulling = false;
		}
	}
</script>

<Pop bind:open placement="bottom-end" width="22rem">
	{#snippet content()}
		{#if partsError}
			<Empty icon={PuzzlePieceIcon} text="Parts failed to list" hint={partsError} />
		{:else if !partsRes}
			<Skeleton rows={2} dot={false} leading="16px" primary="24ch" secondary="20ch" facts={0} action={false} menu={false} />
		{:else if missing.length}
			<Checklist
				bind:value={chosen}
				options={missing.map((p) => ({ value: p.slot, label: p.label || p.slot, description: [p.name, p.sizeBytes ? bytes(p.sizeBytes) : ''].filter(Boolean).join(' · '), disabled: p.error !== '', tip: p.error || [p.repo, p.group].filter(Boolean).join(' ') || undefined }))}
				label="Parts"
				info="Components these weights need that the node lacks, each pulled alongside, none when all are unticked"
			/>
		{:else}
			<span class="secondary">Every part these weights need is stored</span>
		{/if}
		<div class="foot">
			<Tip facts={[{ label: 'weights', value: bytes(groupBytes) }, { label: 'parts', value: bytes(partsBytes) }]} tag="span">
				<span class="sum"><span class="secondary">Total</span><span class="num">{bytes(groupBytes + partsBytes)}</span></span>
			</Tip>
			<span class="fill"></span>
			<NeoButton elevation={2} loading={pulling} disabled={!partsRes} onclick={() => pull(false)}>Pull</NeoButton>
			<NeoButton class="primary" elevation={2} loading={pulling} disabled={!partsRes} onclick={() => pull(true)}>Pull and launch</NeoButton>
		</div>
	{/snippet}
	<RowAction label="Pull" loading={pulling} onclick={() => (open = !open)} />
</Pop>

<style>
	.foot {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-3);
		align-items: center;
		padding-top: var(--nebu-space-2);
		border-top: 1px solid var(--nebu-border);
	}
	.sum {
		display: inline-flex;
		gap: var(--nebu-space-2);
		align-items: baseline;
	}
</style>
