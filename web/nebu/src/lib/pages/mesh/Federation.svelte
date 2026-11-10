<script lang="ts">
	import type { Snippet } from 'svelte';
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ago, enumLabel, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { FederationRole, FederationRoleSchema, type Node } from '#lib/proto/nebu/v1/mesh_pb.js';
	import type { Pool } from '#lib/proto/nebu/v1/pool_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Pop from '#lib/ui/Pop.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { exported, poolLamp, poolShape, poolWord } from '#lib/pages/gateway/gateway.js';
	import { tokenTtlMax } from '#lib/pages/mesh/mesh.js';
	import ArrowsLeftRightIcon from 'phosphor-svelte/lib/ArrowsLeftRightIcon';

	let { federations, id = '', head }: { federations: Node[]; id?: string; head?: Snippet } = $props();

	const open = $derived(federations.find((n) => n.id === id));

	function close() {
		void goto('/mesh/federation');
	}

	let exportTtl = $state(86_400);
	let exporting = $state(false);
	let importOpen = $state(false);
	let importToken = $state('');
	let importing = $state(false);

	function openImport() {
		importToken = '';
		importOpen = true;
	}

	async function copyExport(done: () => void) {
		exporting = true;
		try {
			const res = await rpc.mesh.exportToken({ ttlSeconds: exportTtl });
			await navigator.clipboard.writeText(res.token);
			toast.success('Export token copied', `expires ${when(res.expiresAt)}`);
			done();
		} catch (err) {
			toast.error('Export token failed', reason(err));
		} finally {
			exporting = false;
		}
	}

	async function importMesh() {
		importing = true;
		try {
			const res = await rpc.mesh.importMesh({ token: importToken.trim() });
			toast.success(`Importing from ${res.node?.federation?.meshName || res.node?.name || 'the mesh'}`);
			importOpen = false;
		} catch (err) {
			toast.error('Import failed', reason(err));
		} finally {
			importing = false;
		}
	}

	async function drop(n: Node) {
		try {
			await rpc.mesh.dropFederation({ nodeId: n.id });
			toast.success(`Dropped ${n.federation?.meshName || n.name}`);
			if (id === n.id) close();
		} catch (err) {
			toast.error('Drop failed', reason(err));
		}
	}

	// Pools the federation moves in either direction
	function fedPools(n: Node): { pool: Pool; href: string }[] {
		if (n.federation?.role === FederationRole.EXPORTER) return n.pools.map((p) => ({ pool: p, href: `/gateway/imports/${encodeURIComponent(p.id)}` }));
		return [...live.pools.values()].filter(exported).map((p) => ({ pool: p, href: `/gateway/pools/${encodeURIComponent(p.id)}` }));
	}
</script>

{#snippet list()}
	<Card title="Federations" flush>
		{#snippet tools()}
			<NeoButton class="primary" elevation={2} onclick={openImport}>Import</NeoButton>
			<Pop placement="bottom-end" width="20rem">
				{#snippet content(closePop)}
					<Duration bind:value={exportTtl} base="s" max={tokenTtlMax} label="Valid for" />
					<Row gap={12} justify="flex-end">
						<NeoButton class="primary" elevation={2} loading={exporting} disabled={exportTtl <= 0} onclick={() => copyExport(closePop)}>Copy</NeoButton>
					</Row>
				{/snippet}
				<NeoButton elevation={2}>Export token</NeoButton>
			</Pop>
		{/snippet}
		{#if federations.length === 0}
			<Empty icon={ArrowsLeftRightIcon} text="No federations" />
		{:else}
			<List head={{ primary: 'Mesh', secondary: 'Address', facts: ['Since'] }}>
				{#each federations as n (n.id)}
					{@const fed = n.federation}
					<ListRow
						primary={fed?.meshName || n.name}
						primaryTip={[fed?.fingerprint ? `fingerprint ${fed.fingerprint}` : '', fed?.meshHash ? `hash ${fed.meshHash}` : ''].filter(Boolean).join('\n') || undefined}
						secondary={fed?.address || n.address}
						secondaryKind="address"
						secondaryWidth="24ch"
						facts={[fed?.since ? { value: ago(fed.since, clockTick.now), tip: when(fed.since) } : { value: '' }]}
						selected={n.id === id}
						onclick={() => goto(`/mesh/federation/${encodeURIComponent(n.id)}`)}
						menu={[{ label: 'Drop', danger: true, confirm: `Drop ${fed?.meshName || n.name}`, onclick: () => drop(n) }]}
					>
						{#snippet badges()}<StatePill label={enumLabel(FederationRoleSchema, fed?.role ?? FederationRole.UNSPECIFIED)} lamp="primary" />{/snippet}
					</ListRow>
				{/each}
			</List>
		{/if}
	</Card>
{/snippet}

<Sheet open={id !== ''} onclose={close} {head} {list}>
	{#if open}
		{@const fed = open.federation}
		{@const pools = fedPools(open)}
		<PanelHeader title={fed?.meshName || open.name} subtitle={enumLabel(FederationRoleSchema, fed?.role ?? FederationRole.UNSPECIFIED)} onclose={close} />
		<Stack gap={24}>
			<KeyRows
				rows={[
					{ label: 'node', value: open.name },
					{ label: 'address', value: fed?.address || open.address, mono: true },
					{ label: 'since', value: fed?.since ? ago(fed.since, clockTick.now) : '', tip: when(fed?.since) || undefined },
					{ label: 'fingerprint', value: fed?.fingerprint ?? '', mono: true, copy: true, tip: fed?.fingerprint },
					{ label: 'mesh hash', value: fed?.meshHash ?? '', mono: true, copy: true, tip: fed?.meshHash }
				]}
			/>
			<Section title="Pools" level={3}>
				{#if pools.length}
					<List head={{ primary: 'Pool', secondary: 'Model', facts: ['State'] }}>
						{#each pools as { pool, href } (pool.id)}
							<ListRow lamp={poolLamp(pool)} shape={poolShape(pool)} primary={pool.id} secondary={pool.spec?.model ?? ''} secondaryKind="repo" facts={[{ value: poolWord(pool) }]} onclick={() => goto(href)} />
						{/each}
					</List>
				{:else}
					<Empty text="No pools" row />
				{/if}
			</Section>
		</Stack>
	{:else}
		<PanelHeader title="Federation" onclose={close} />
		<Empty text="No such federation" />
	{/if}
</Sheet>

<Dialog bind:open={importOpen} title="Import a mesh" confirm="Import" busy={importing} disabled={!importToken.trim()} onconfirm={importMesh}>
	<Text bind:value={importToken} label="Export token" mono required />
</Dialog>
