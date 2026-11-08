<script lang="ts">
	import { untrack } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { Part } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import type { StoredModel } from '#lib/proto/nebu/v1/store_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import AsteriskIcon from 'phosphor-svelte/lib/AsteriskIcon';
	import PuzzlePieceIcon from 'phosphor-svelte/lib/PuzzlePieceIcon';

	let { model }: { model: StoredModel } = $props();

	let parts = $state<Part[] | undefined>(undefined);
	let error = $state('');
	let pulling = $state(false);
	let abort: AbortController | undefined;

	const missing = $derived((parts ?? []).filter((p) => p.required && !p.stored && !p.bundled));
	// Changes only when another group is shown, not when the stream refreshes this one
	const key = $derived([model.sourceId, model.repo, model.revision, model.group].join('\n'));

	async function load(m: StoredModel) {
		abort?.abort();
		const ac = new AbortController();
		abort = ac;
		try {
			const res = await rpc.estimate.parts({ sourceId: m.sourceId, repo: m.repo, revision: m.revision, group: m.group }, { signal: ac.signal });
			if (ac.signal.aborted) return;
			parts = res.parts;
			error = '';
		} catch (err) {
			if (ac.signal.aborted) return;
			error = reason(err);
		}
	}

	$effect(() => {
		void key;
		parts = undefined;
		error = '';
		void load(untrack(() => model));
		return () => abort?.abort();
	});

	// A part pulled elsewhere turns stored, so the list reloads when the store changes
	$effect(() => {
		void key;
		const m = untrack(() => model);
		let timer: ReturnType<typeof setTimeout> | undefined;
		const stop = live.on(EventKind.MODEL, () => {
			clearTimeout(timer);
			timer = setTimeout(() => void load(m), 300);
		});
		return () => {
			clearTimeout(timer);
			stop();
		};
	});

	async function pull() {
		pulling = true;
		try {
			const res = await rpc.store.pull({ sourceId: model.sourceId, repo: model.repo, revision: model.revision, group: model.group });
			followTask(res.task, `Pull parts of ${model.repo}`);
		} catch (err) {
			toast.error('Pull failed', reason(err));
		} finally {
			pulling = false;
		}
	}

	function stateOf(p: Part): string {
		return p.stored ? 'stored' : p.bundled ? 'bundled' : 'missing';
	}

	function componentTip(p: Part): string {
		return [p.name, p.repo ? `${p.repo}${p.group ? ` ${p.group}` : ''}` : '', p.sizeBytes ? bytes(p.sizeBytes) : '', p.error].filter(Boolean).join('\n');
	}
</script>

{#if error}
	<Empty icon={PuzzlePieceIcon} text="Parts failed to list" hint={error} />
{:else if !parts}
	<Skeleton rows={2} primary="16ch" secondary="24ch" facts={1} action={false} menu={false} />
{:else if parts.length === 0}
	<Empty icon={PuzzlePieceIcon} text="No parts" hint="This group loads on its own, with nothing from other repositories" />
{:else}
	<List head={{ primary: 'Slot', secondary: 'Component', facts: ['State'] }}>
		{#each parts as p (p.slot)}
			<ListRow lamp={p.stored || p.bundled ? 'success' : p.required ? 'error' : 'muted'} shape={p.stored || p.bundled ? 'filled' : 'hollow'} primary={p.label || p.slot} primaryWidth="16ch" secondary={p.name || p.repo} secondaryTip={componentTip(p)} secondaryWidth="24ch" facts={[{ value: stateOf(p) }]}>
				{#snippet badges()}
					{#if p.required}
						<Tip text="Required" tag="span"><span class="mark" aria-label="Required"><AsteriskIcon size={16} /></span></Tip>
					{/if}
				{/snippet}
			</ListRow>
		{/each}
	</List>
	{#if missing.length}
		<div class="actions"><NeoButton elevation={2} loading={pulling} onclick={pull}>Pull parts</NeoButton></div>
	{/if}
{/if}

<style>
	.mark {
		display: inline-flex;
		color: var(--nebu-text-muted);
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		padding-top: var(--nebu-space-3);
	}
</style>
