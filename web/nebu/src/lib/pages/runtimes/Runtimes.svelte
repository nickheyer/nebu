<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { reason, rpc } from '#lib/api/transport.js';
	import { enumLabel, plural } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { ModelKindSchema } from '#lib/proto/nebu/v1/model_pb.js';
	import { ApiFlavorSchema, type RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import { compactBelow } from '#lib/ui/list.svelte.js';
	import RuntimePanel from '#lib/pages/runtimes/RuntimePanel.svelte';
	import { serveTabs } from '#lib/pages/serve/tabs.js';
	import EngineIcon from 'phosphor-svelte/lib/EngineIcon';

	let { id = '', tab = 'installs' }: { id?: string; tab?: string } = $props();

	let runtimes = $state<RuntimeStatus[]>([]);
	let loaded = $state(false);
	let error = $state('');
	let listWidth = $state(0);
	const slim = $derived(id !== '' && listWidth > 0 && listWidth < compactBelow);

	async function load() {
		try {
			runtimes = (await rpc.runtimes.listRuntimes({})).runtimes;
			error = '';
		} catch (err) {
			error = reason(err);
			toast.error('Runtimes failed to list', error);
		} finally {
			loaded = true;
		}
	}

	onMount(() => {
		void load();
		const stops = [EventKind.INSTALL, EventKind.HOST, EventKind.BUILD].map((k) => live.on(k, () => void load()));
		return () => stops.forEach((s) => s());
	});

	const sorted = $derived([...runtimes].sort((a, b) => Number(b.compatible) - Number(a.compatible) || (a.runtime?.name ?? '').localeCompare(b.runtime?.name ?? '')));
	const selected = $derived(runtimes.find((r) => r.runtime?.id === id));
	// How many installs this host holds of each runtime
	const installsBy = $derived.by(() => {
		const by = new Map<string, number>();
		for (const i of live.installs.values()) by.set(i.runtimeId, (by.get(i.runtimeId) ?? 0) + 1);
		return by;
	});

	function installs(r: RuntimeStatus): number {
		return installsBy.get(r.runtime?.id ?? '') ?? 0;
	}

	// The install count, or what keeps the host from running it
	function status(r: RuntimeStatus): string {
		if (!r.compatible) return r.unmet[0] ?? 'incompatible';
		const n = installs(r);
		return n > 0 ? plural(n, 'install') : '-';
	}

	function about(r: RuntimeStatus): string {
		const rt = r.runtime;
		return [rt?.description ?? '', rt?.formats.length ? `formats ${rt.formats.join(', ')}` : '', `api ${enumLabel(ApiFlavorSchema, rt?.api ?? 0)}`, `model kind ${enumLabel(ModelKindSchema, rt?.kind ?? 0)}`].filter(Boolean).join('\n');
	}

	function href(runtimeId: string, t: string): string {
		return `/runtimes/${encodeURIComponent(runtimeId)}${t === 'installs' ? '' : `/${t}`}`;
	}

	function open(r: RuntimeStatus, t = 'installs') {
		void goto(href(r.runtime?.id ?? '', t));
	}

	function showTab(t: string) {
		void goto(href(id, t), { replace: true, reset: false });
	}
</script>

{#snippet list()}
	<div class="catalog" bind:clientWidth={listWidth}>
		<Card flush>
			{#if !loaded}
				<Skeleton rows={5} secondary="16ch" facts={1} menu={false} />
			{:else if error && runtimes.length === 0}
				<Empty text="Runtimes failed to list" hint={error} action="Retry" onclick={() => void load()} />
			{:else if sorted.length === 0}
				<Empty icon={EngineIcon} text="No runtimes" />
			{:else}
				<List head={{ primary: 'Runtime', secondary: 'Installs' }}>
					{#each sorted as r (r.runtime?.id)}
						{@const n = installs(r)}
						<ListRow
							lamp={r.compatible ? 'success' : 'muted'}
							shape={r.compatible && n > 0 ? 'filled' : 'hollow'}
							primary={r.runtime?.name ?? ''}
							primaryTip={about(r)}
							secondary={slim ? undefined : status(r)}
							secondaryKind="message"
							secondaryWidth="28ch"
							secondaryTip={r.compatible ? undefined : r.unmet.join('\n')}
							selected={r.runtime?.id === id}
							onclick={() => open(r)}
						>
							{#snippet action()}
								{#if r.compatible}<RowAction onclick={() => open(r, 'install')} label="Install" />{/if}
							{/snippet}
						</ListRow>
					{/each}
				</List>
			{/if}
		</Card>
	</div>
{/snippet}

<PageHeader tabs={serveTabs} active="runtimes" info="Engines this host can run models with, each holding its installs, builds and parameter defaults" />

<Sheet open={id !== ''} onclose={() => goto('/runtimes')} {list}>
	{#if selected}
		{#key selected.runtime?.id}
			<RuntimePanel status={selected} {tab} ontab={showTab} onclose={() => goto('/runtimes')} />
		{/key}
	{:else if !loaded}
		<Skeleton rows={4} kind="facts" />
	{:else}
		<Empty text="No such runtime" action="Back" href="/runtimes" />
	{/if}
</Sheet>

<style>
	.catalog {
		min-width: 0;
	}
</style>
