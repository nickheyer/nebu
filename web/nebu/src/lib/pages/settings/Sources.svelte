<script lang="ts">
	import { onMount } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import type { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { enumLabel } from '#lib/format.js';
	import { SourceKindSchema, type Provider, type SourceStatus } from '#lib/proto/nebu/v1/source_pb.js';
	import type { HeaderTab } from '#lib/shell/chrome.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Dot from '#lib/ui/Dot.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import type { Lamp } from '#lib/ui/status.js';
	import SourceEditor from '#lib/pages/settings/SourceEditor.svelte';
	import DatabaseIcon from 'phosphor-svelte/lib/DatabaseIcon';

	let { tabs, sources }: { tabs: HeaderTab[]; sources: ListQuery<SourceStatus, Record<string, never>> } = $props();

	let providers = $state<Provider[]>([]);
	let open = $state<string | undefined>(undefined);

	onMount(() => {
		rpc.sources
			.listProviders({})
			.then((r) => (providers = r.providers))
			.catch((err) => toast.error('Providers failed to list', reason(err)));
	});

	async function remove(id: string) {
		try {
			await rpc.sources.deleteSource({ id });
			toast.success(`Deleted ${id}`);
			if (open === id) open = undefined;
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}

	function lampOf(s: SourceStatus): Lamp {
		if (s.error) return 'error';
		if (s.capabilities?.authRequired && !s.capabilities.tokenPresent) return 'warning';
		return 'success';
	}

	function statusOf(s: SourceStatus): string {
		const c = s.capabilities;
		if (s.error) return 'failed';
		if (c?.authRequired && !c.tokenPresent) return 'token missing';
		if (c?.tokenPresent) return 'token set';
		return 'ready';
	}

	function providerName(s: SourceStatus): string {
		const kind = s.source?.kind ?? 0;
		return s.capabilities?.name || providers.find((p) => p.kind === kind)?.name || enumLabel(SourceKindSchema, kind);
	}

	function capabilities(s: SourceStatus): string {
		const c = s.capabilities;
		if (!c) return '';
		const on = [c.browse && 'browse', c.search && 'search', c.paginate && 'paginate', c.card && 'card', c.revisions && 'revisions'].filter(Boolean);
		return on.join(', ');
	}

	function bare(endpoint: string): string {
		return endpoint.replace(/^[a-z][a-z0-9+.-]*:\/\//i, '').replace(/\/$/, '');
	}
</script>

{#snippet list()}
	<Card title="Sources" flush>
		{#snippet tools()}
			<NeoButton class="primary" elevation={2} onclick={() => (open = '')}>Add source</NeoButton>
		{/snippet}
		{#if !sources.settled}
			<Skeleton rows={6} secondary="16ch" facts={2} action={false} />
		{:else if sources.error}
			<Empty text={sources.error} action="Retry" onclick={() => sources.refresh()} />
		{:else if sources.items.length === 0}
			<Empty icon={DatabaseIcon} text="No sources" action={open === undefined ? 'Add source' : undefined} onclick={() => (open = '')} />
		{:else}
			<List head={{ primary: 'Source', secondary: 'Provider', facts: ['Endpoint', 'Status'] }}>
				{#each sources.items as s (s.source?.id)}
					{@const id = s.source?.id ?? ''}
					{@const endpoint = s.capabilities?.endpoint ?? ''}
					<ListRow
						primary={id}
						secondary={providerName(s)}
						secondaryTip={capabilities(s) || undefined}
						secondaryWidth="20ch"
						facts={[
							{ value: bare(endpoint), tip: endpoint || undefined, mono: true },
							{ value: statusOf(s), tip: s.error || undefined }
						]}
						selected={open === id}
						onclick={() => (open = id)}
						menu={s.source?.seeded ? undefined : [{ label: 'Delete', danger: true, confirm: `Delete ${id}?`, onclick: () => remove(id) }]}
					>
						{#snippet leading()}<Dot lamp={lampOf(s)} label={statusOf(s)} />{/snippet}
					</ListRow>
				{/each}
			</List>
			<LoadMore next={sources.next} loading={sources.loading} onmore={() => sources.more()} />
		{/if}
	</Card>
{/snippet}

<PageHeader {tabs} active="sources" />

<Sheet open={open !== undefined} onclose={() => (open = undefined)} {list}>
	{#if open !== undefined}
		{#key open}
			<SourceEditor status={open ? sources.items.find((s) => s.source?.id === open) : undefined} {providers} onclose={() => (open = undefined)} />
		{/key}
	{/if}
</Sheet>
