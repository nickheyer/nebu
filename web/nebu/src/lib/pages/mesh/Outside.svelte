<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ago, enumLabel, when } from '#lib/format.js';
	import { AdmissionSide, AdmissionState, AdmissionStateSchema, type Admission, type MeshStatus, type NearbyMesh } from '#lib/proto/nebu/v1/mesh_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Split from '#lib/ui/layout/Split.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { admissionLamp } from '#lib/ui/status.js';
	import { admissionTip, members, membership, settled } from '#lib/pages/mesh/mesh.js';

	let { status, oncreate, onjoin }: { status: MeshStatus | undefined; oncreate: () => void; onjoin: () => void } = $props();

	let asking = $state('');
	const meshes = $derived(status?.meshes ?? []);
	const nodes = $derived(status?.nodes ?? []);
	const admissions = $derived(status?.admissions ?? []);

	// Asks a member heard nearby to let this node in, or accepts the invitation the admission carries
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
</script>

{#snippet meshesSection()}
	<Section title="Nearby meshes" gap={0} count={meshes.length}>
		{#each meshes as m (m.hash)}
			{@const via = m.nodes[0]}
			<ListRow primary={m.name} primaryTip={meshTip(m)} secondary={members(m.members)} secondaryWidth="12ch">
				{#snippet action()}
					<NeoButton text loading={asking === m.hash} disabled={!via} onclick={() => ask(m.hash, m.hash, via?.id ?? '', via?.address ?? '')}>Ask</NeoButton>
				{/snippet}
			</ListRow>
		{:else}
			<Empty text="None heard" />
		{/each}
	</Section>
{/snippet}

{#snippet nodesSection()}
	<Section title="Nearby nodes" gap={0} count={nodes.length}>
		{#each nodes as n (n.id)}
			<ListRow primary={n.name} primaryTip={[`${n.os} ${n.arch}`.trim(), n.version, `heard ${when(n.heardAt)}`].filter(Boolean).join('\n')} secondary={n.address || n.from} secondaryKind="address" secondaryWidth="24ch" facts={[{ value: membership(n) }]} />
		{:else}
			<Empty text="None heard" />
		{/each}
	</Section>
{/snippet}

{#snippet admissionsSection()}
	<Section title="Admissions" gap={0} count={admissions.length}>
		{#each admissions as a (a.id)}
			<ListRow
				primary={a.meshName}
				primaryTip={admissionTip(a)}
				secondary="via {a.node?.name || a.node?.address || 'unknown'}"
				secondaryWidth="24ch"
				facts={a.expiresAt && !settled(a) ? [{ value: `expires ${ago(a.expiresAt, clockTick.now)}`, tip: when(a.expiresAt) }] : []}
			>
				{#snippet badges()}<StatePill label={enumLabel(AdmissionStateSchema, a.state)} lamp={admissionLamp(a.state)} />{/snippet}
				{#snippet action()}
					{#if invitation(a)}
						<NeoButton text loading={asking === a.id} onclick={() => ask(a.id, a.meshHash, a.node?.id ?? '', a.node?.address ?? '')}>Accept</NeoButton>
						<NeoButton text onclick={() => decide('decline', a)}>Decline</NeoButton>
					{:else if settled(a)}
						<NeoButton text onclick={() => decide('dismiss', a)}>Dismiss</NeoButton>
					{/if}
				{/snippet}
			</ListRow>
		{:else}
			<Empty text="None under way" />
		{/each}
	</Section>
{/snippet}

<Stack gap={24}>
	<Row gap={16}>
		<Cell grow={false}><h1>No mesh</h1></Cell>
		<Cell grow={false} push>
			{#if status?.listen}
				<Row gap={8} wrap={false}>
					<span class="secondary">listening</span>
					<Str value={status.listen} kind="address" />
				</Row>
			{:else}
				<span class="secondary">not listening</span>
			{/if}
		</Cell>
	</Row>
	<Row gap={12}>
		<Cell grow={false}><NeoButton class="primary" elevation={2} onclick={oncreate}>Create a mesh</NeoButton></Cell>
		<Cell grow={false}><NeoButton elevation={2} onclick={onjoin}>Join with a token</NeoButton></Cell>
	</Row>
	{#if viewport.ultrawide}
		<Split ratio="3/5">
			{#snippet left()}
				{@render meshesSection()}
				{@render nodesSection()}
			{/snippet}
			{#snippet right()}{@render admissionsSection()}{/snippet}
		</Split>
	{:else}
		{@render meshesSection()}
		{@render nodesSection()}
		{@render admissionsSection()}
	{/if}
</Stack>
