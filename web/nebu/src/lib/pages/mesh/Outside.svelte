<script lang="ts">
	import type { Timestamp } from '@bufbuild/protobuf/wkt';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ago, enumLabel, when } from '#lib/format.js';
	import { AdmissionSide, AdmissionState, AdmissionStateSchema, type Admission, type MeshStatus, type NearbyMesh } from '#lib/proto/nebu/v1/mesh_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Time from '#lib/ui/Time.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import Columns from '#lib/ui/layout/Columns.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { admissionLamp } from '#lib/ui/status.js';
	import { admissionTip, members, membership, settled } from '#lib/pages/mesh/mesh.js';
	import ShareNetworkIcon from 'phosphor-svelte/lib/ShareNetworkIcon';

	// This node outside any mesh, with what it hears nearby and the admissions it has under way
	let { status }: { status: MeshStatus | undefined } = $props();

	const meshes = $derived(status?.meshes ?? []);
	const nodes = $derived(status?.nodes ?? []);
	const admissions = $derived(status?.admissions ?? []);
	const heard = $derived(meshes.length > 0 || nodes.length > 0);

	let asking = $state('');

	// Asks a member heard nearby to let this node in, or accepts the invitation an admission carries
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

	// Create a mesh, then show its join token once with its expiry
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

	// Join the mesh a token names
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
	<Card title="Nearby meshes" count={meshes.length} flush>
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
	<Card title="Nearby nodes" count={nodes.length} flush>
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
	<Card title="Admissions" count={admissions.length} flush>
		<List head={{ primary: 'Mesh', secondary: 'Through', facts: ['Expires'] }}>
			{#each admissions as a (a.id)}
				<ListRow
					primary={a.meshName}
					primaryTip={admissionTip(a)}
					secondary={a.node?.name || a.node?.address || 'unknown'}
					secondaryWidth="20ch"
					facts={[a.expiresAt && !settled(a) ? { value: ago(a.expiresAt, clockTick.now), tip: when(a.expiresAt) } : { value: '' }]}
					menu={invitation(a) ? [{ label: 'Decline', danger: true, confirm: `Decline ${a.meshName}`, onclick: () => decide('decline', a) }] : undefined}
				>
					{#snippet badges()}<StatePill label={enumLabel(AdmissionStateSchema, a.state)} lamp={admissionLamp(a.state)} />{/snippet}
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

<PageHeader>
	{#snippet meta()}
		<span>No mesh</span>
		{#if status?.listen}
			<span>listening <Str value={status.listen} kind="address" /></span>
		{:else}
			<span>not listening</span>
		{/if}
	{/snippet}
	{#snippet actions()}
		<NeoButton elevation={2} onclick={openJoin}>Join with a token</NeoButton>
		<NeoButton class="primary" elevation={2} onclick={openCreate}>Create a mesh</NeoButton>
	{/snippet}
</PageHeader>

{#if !heard && admissions.length === 0}
	<Card>
		<Empty icon={ShareNetworkIcon} text="Not in a mesh" hint="A mesh joins nebu hosts so they share models and serve one model across machines. Hosts heard nearby show here." />
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
		<span>Shown once, expires <Time value={made.expires} /></span>
	{/if}
{/snippet}

<Dialog
	bind:open={createOpen}
	title={made ? `Join token for ${made.name}` : 'Create a mesh'}
	confirm={made ? undefined : 'Create'}
	busy={creating}
	disabled={!meshName.trim()}
	onconfirm={createMesh}
	footer={made ? madeFooter : undefined}
>
	{#if made}
		<Screen><span class="token">{made.token}</span></Screen>
	{:else}
		<Text bind:value={meshName} label="Name" max={64} required />
	{/if}
</Dialog>

<Dialog bind:open={joinOpen} title="Join with a token" confirm="Join" busy={joining} disabled={!joinToken.trim()} onconfirm={join}>
	<Text bind:value={joinToken} label="Token" mono required />
</Dialog>

<style>
	.token {
		display: block;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
</style>
