<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { enumLabel, plural } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { ModelKindSchema } from '#lib/proto/nebu/v1/model_pb.js';
	import { ApiFlavorSchema, type RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import RuntimePanel from '#lib/pages/runtimes/RuntimePanel.svelte';

	let { id = '', tab = 'installs' }: { id?: string; tab?: string } = $props();

	let runtimes = $state<RuntimeStatus[]>([]);
	let loaded = $state(false);
	let error = $state('');

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
	const counts = $derived.by(() => {
		const by = new Map<string, number>();
		for (const i of live.installs.values()) by.set(i.runtimeId, (by.get(i.runtimeId) ?? 0) + 1);
		return by;
	});

	function phrase(r: RuntimeStatus): string {
		if (!r.compatible) return r.unmet[0] ?? 'incompatible';
		const n = counts.get(r.runtime?.id ?? '') ?? 0;
		return n === 0 ? 'no install' : plural(n, 'install');
	}

	function about(r: RuntimeStatus): string {
		const rt = r.runtime;
		return [rt?.description ?? '', `formats ${rt?.formats.join(', ') || 'none'}`, `api ${enumLabel(ApiFlavorSchema, rt?.api ?? 0)}`, `model kind ${enumLabel(ModelKindSchema, rt?.kind ?? 0)}`].filter(Boolean).join('\n');
	}

	// The panel route, the installs tab being the bare runtime path
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
	{#if !loaded}
		<Skeleton rows={5} />
	{:else if error && runtimes.length === 0}
		<Empty text={error} action="Retry" onclick={() => void load()} />
	{:else if sorted.length === 0}
		<Empty text="No runtimes" />
	{:else}
		<Stack gap={0}>
			{#each sorted as r (r.runtime?.id)}
				{@const installable = r.compatible && r.installs.length > 0}
				<ListRow
					lamp={r.compatible ? 'success' : 'muted'}
					shape={r.compatible ? 'filled' : 'hollow'}
					primary={r.runtime?.name ?? ''}
					primaryTip={about(r)}
					secondary={phrase(r)}
					secondaryKind="message"
					secondaryWidth="64ch"
					selected={r.runtime?.id === id}
					onclick={() => open(r)}
					menu={installable ? [{ label: 'Open', onclick: () => open(r) }] : undefined}
				>
					{#snippet action()}
						{#if installable}
							<NeoButton text onclick={() => open(r, 'install')}>Install</NeoButton>
						{:else}
							<NeoButton text onclick={() => open(r)}>Open</NeoButton>
						{/if}
					{/snippet}
				</ListRow>
			{/each}
		</Stack>
	{/if}
{/snippet}

<Sheet open={id !== ''} onclose={() => goto('/runtimes')} {list} empty="Select a runtime">
	{#if selected}
		{#key selected.runtime?.id}
			<RuntimePanel status={selected} {tab} ontab={showTab} onclose={() => goto('/runtimes')} />
		{/key}
	{:else if !loaded}
		<Skeleton rows={4} height={32} />
	{:else}
		<Empty text="No such runtime" action="Back" href="/runtimes" />
	{/if}
</Sheet>
