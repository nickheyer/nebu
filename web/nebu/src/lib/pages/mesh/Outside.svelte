<script lang="ts">
	import type { Timestamp } from '@bufbuild/protobuf/wkt';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ago, enumLabel, num, plural, shortId, when } from '#lib/format.js';
	import { AdmissionSide, AdmissionState, AdmissionStateSchema, type Admission, type MeshStatus, type NearbyMesh } from '#lib/proto/nebu/v1/mesh_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import StateWord from '#lib/ui/StateWord.svelte';
	import Time from '#lib/ui/Time.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Facts, { type FactItem } from '#lib/ui/Facts.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import Columns from '#lib/ui/layout/Columns.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { admissionLamp } from '#lib/ui/status.js';
	import { admissionTip, members, membership, settled } from '#lib/pages/mesh/mesh.js';
	import ShareNetworkIcon from 'phosphor-svelte/lib/ShareNetworkIcon';

	// This node outside any mesh with what it hears nearby
	let { status }: { status: MeshStatus | undefined } = $props();

	const meshes = $derived(status?.meshes ?? []);
	const nodes = $derived(status?.nodes ?? []);
	const admissions = $derived(status?.admissions ?? []);
	// The node by name and id, where it listens, where others reach it, and whether it calls out to the network
	const self = $derived.by((): FactItem[] => {
		const s = status;
		if (!s) return [];
		return [
			{ id: 'name', label: 'Name', value: s.nodeName || 'unnamed', note: s.nodeId ? `id ${shortId(s.nodeId)}` : undefined, tip: s.nodeId || undefined, wide: true },
			{ id: 'listen', label: 'Listening on', value: s.listen || 'nothing', mono: s.listen !== '', quiet: !s.listen, lamp: s.listen ? undefined : 'warning', note: s.listen ? undefined : 'no other node can call it', wide: s.listen !== '' },
			{ id: 'address', label: 'Reached at', value: s.address || 'nowhere', mono: s.address !== '', quiet: !s.address, note: s.address ? undefined : s.listen ? 'it listens on this machine alone' : 'nothing listens', wide: s.address !== '' },
			{ id: 'announce', label: 'Beacons', value: s.announce ? 'on' : 'off', lamp: s.announce ? 'success' : undefined, quiet: !s.announce, note: s.announce ? 'nodes nearby hear it' : 'it stays silent' },
			{ id: 'heard', label: 'Heard nearby', value: num(meshes.length + nodes.length), note: `${plural(meshes.length, 'mesh', 'meshes')}, ${plural(nodes.length, 'node')}` }
		];
	});
	const heard = $derived(meshes.length > 0 || nodes.length > 0);

	let asking = $state('');

	// Asks a member heard nearby to let this node in
	async function ask(key: string, meshHash: string, nodeId: string, address: string) {
		asking = key;
		try {
			await rpc.mesh.ask({ meshHash, nodeId, address });
			toast.success('Asked to join');
		} catch (err) {
			toast.error('Ask failed', reason(err));
		} finally {
			asking = '';
		}
	}

	async function decide(action: 'decline' | 'dismiss', a: Admission) {
		try {
			if (action === 'decline') await rpc.mesh.refuse({ admissionId: a.id });
			else await rpc.mesh.dismiss({ admissionId: a.id });
		} catch (err) {
			toast.error(action === 'decline' ? 'Decline failed' : 'Dismiss failed', reason(err));
		}
	}

	function meshTip(m: NearbyMesh): string {
		return [`heard ${when(m.heardAt)}`, ...m.nodes.map((n) => `${n.name} ${n.address || n.from}`.trim())].join('\n');
	}

	function invitation(a: Admission): boolean {
		return a.state === AdmissionState.PENDING && a.side === AdmissionSide.CANDIDATE && a.invited;
	}

	let createOpen = $state(false);
	let meshName = $state('');
	let made = $state<{ name: string; token: string; expires: Timestamp | undefined } | undefined>(undefined);
	let creating = $state(false);

	function openCreate() {
		meshName = '';
		made = undefined;
		createOpen = true;
	}

	async function createMesh() {
		const name = meshName.trim();
		creating = true;
		try {
			await rpc.mesh.init({ name });
			toast.success(`Created ${name}`);
		} catch (err) {
			toast.error('Create failed', reason(err));
			creating = false;
			return;
		}
		try {
			const res = await rpc.mesh.token({ ttlSeconds: 0 });
			made = { name, token: res.token, expires: res.expiresAt };
		} catch (err) {
			toast.error('Join token failed', reason(err));
			createOpen = false;
		} finally {
			creating = false;
		}
	}

	let joinOpen = $state(false);
	let joinToken = $state('');
	let joining = $state(false);

	function openJoin() {
		joinToken = '';
		joinOpen = true;
	}

	async function join() {
		joining = true;
		try {
			const res = await rpc.mesh.join({ token: joinToken.trim() });
			toast.success(`Joined ${res.mesh?.name ?? 'the mesh'}`);
			joinOpen = false;
		} catch (err) {
			toast.error('Join failed', reason(err));
		} finally {
			joining = false;
		}
	}
</script>

{#snippet meshesCard()}
	<Card title="Nearby meshes" flush>
		<List head={{ primary: 'Mesh', secondary: 'Members' }}>
			{#each meshes as m (m.hash)}
				{@const via = m.nodes[0]}
				<ListRow primary={m.name} primaryTip={meshTip(m)} secondary={members(m.members)} secondaryWidth="12ch">
					{#snippet action()}
						<RowAction loading={asking === m.hash} disabled={!via} onclick={() => ask(m.hash, m.hash, via?.id ?? '', via?.address ?? '')} label="Ask" />
					{/snippet}
				</ListRow>
			{/each}
		</List>
	</Card>
{/snippet}

{#snippet nodesCard()}
	<Card title="Nearby nodes" flush>
		<List head={{ primary: 'Node', secondary: 'Address', facts: ['Mesh'] }}>
			{#each nodes as n (n.id)}
				<ListRow
					primary={n.name}
					primaryTip={[`${n.os} ${n.arch}`.trim(), n.version, `heard ${when(n.heardAt)}`].filter(Boolean).join('\n')}
					secondary={n.address || n.from}
					secondaryKind="address"
					secondaryWidth="24ch"
					facts={[{ value: membership(n) }]}
				/>
			{/each}
		</List>
	</Card>
{/snippet}

{#snippet admissionsCard()}
	<Card title="Admissions" flush>
		<List head={{ primary: 'Mesh', secondary: 'Through', facts: ['Expires'] }}>
			{#each admissions as a (a.id)}
				<ListRow
					primary={a.meshName}
					primaryTip={admissionTip(a)}
					secondary={a.node?.name || a.node?.address || ''}
					secondaryWidth="20ch"
					facts={[a.expiresAt && !settled(a) ? { value: ago(a.expiresAt, clockTick.now), tip: when(a.expiresAt) } : { value: '' }]}
					menu={invitation(a) ? [{ label: 'Decline', danger: true, confirm: `Decline ${a.meshName}`, onclick: () => decide('decline', a) }] : undefined}
				>
					{#snippet badges()}<StateWord label={enumLabel(AdmissionStateSchema, a.state)} lamp={admissionLamp(a.state)} />{/snippet}
					{#snippet action()}
						{#if invitation(a)}
							<RowAction loading={asking === a.id} onclick={() => ask(a.id, a.meshHash, a.node?.id ?? '', a.node?.address ?? '')} label="Accept" />
						{:else if settled(a)}
							<RowAction onclick={() => decide('dismiss', a)} label="Dismiss" />
						{/if}
					{/snippet}
				</ListRow>
			{/each}
		</List>
	</Card>
{/snippet}

<PageHeader />

<!-- What this node is and how others reach it, with the two ways into a mesh -->
<Card title="This node">
	{#snippet tools()}
		<NeoButton elevation={2} onclick={openJoin}>Join with a token</NeoButton>
		<NeoButton class="primary" elevation={2} onclick={openCreate}>Create a mesh</NeoButton>
	{/snippet}
	<Facts items={self} />
</Card>

{#if !heard && admissions.length === 0}
	<Card title="Nearby">
		<Empty icon={ShareNetworkIcon} text="No node or mesh heard on this network" hint={status?.announce ? 'Nodes on the same network find each other by beacon. One further away joins with a token made by a member of the mesh.' : 'Beacons are off on this node, so it hears no other and no other hears it. A node joins a mesh with a token made by one of its members.'} />
	</Card>
{:else if heard && admissions.length}
	<Columns ratio="3/2">
		{#snippet left()}
			{#if meshes.length}{@render meshesCard()}{/if}
			{#if nodes.length}{@render nodesCard()}{/if}
		{/snippet}
		{#snippet right()}{@render admissionsCard()}{/snippet}
	</Columns>
{:else}
	{#if meshes.length}{@render meshesCard()}{/if}
	{#if nodes.length}{@render nodesCard()}{/if}
	{#if admissions.length}{@render admissionsCard()}{/if}
{/if}

{#snippet madeFooter()}
	{#if made}
		<CopyButton text={made.token} label="Copy join token" />
		<span>Shown once · expires <Time value={made.expires} /></span>
	{/if}
{/snippet}

<Dialog
	bind:open={createOpen}
	title={made ? `Join token for ${made.name}` : 'Create a mesh'}
	confirm={made ? undefined : 'Create'}
	busy={creating}
	needs={made || meshName.trim() ? '' : 'Name the mesh'}
	onconfirm={createMesh}
	footer={made ? madeFooter : undefined}
>
	{#if made}
		<Screen><span class="token">{made.token}</span></Screen>
	{:else}
		<Text bind:value={meshName} label="Name" max={64} required />
	{/if}
</Dialog>

<Dialog bind:open={joinOpen} title="Join with a token" confirm="Join" busy={joining} needs={joinToken.trim() ? '' : 'Paste the join token'} onconfirm={join}>
	<p class="secondary">A member of the mesh makes the token on its Mesh page, under Token, and it works until the time it was given runs out.</p>
	<Secret bind:value={joinToken} label="Join token" required />
</Dialog>

<style>
	.token {
		display: block;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
</style>
