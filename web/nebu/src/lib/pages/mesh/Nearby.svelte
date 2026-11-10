<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ago, enumLabel, when } from '#lib/format.js';
	import { AdmissionSide, AdmissionState, AdmissionStateSchema, type Admission, type MeshStatus } from '#lib/proto/nebu/v1/mesh_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import Columns from '#lib/ui/layout/Columns.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { admissionLamp } from '#lib/ui/status.js';
	import { admissionTip, members, membership, outside, settled, sideWord } from '#lib/pages/mesh/mesh.js';
	import BroadcastIcon from 'phosphor-svelte/lib/BroadcastIcon';

	let { status }: { status: MeshStatus | undefined } = $props();

	const nodes = $derived(status?.nodes ?? []);
	const meshes = $derived(status?.meshes ?? []);
	const admissions = $derived(status?.admissions ?? []);
	const heard = $derived(nodes.length > 0 || meshes.length > 0);

	let inviteOpen = $state(false);
	let inviteAddress = $state('');
	let inviting = $state<string | undefined>(undefined);
	const inviteValid = $derived(/^[^\s:[\]]+:\d{1,5}$|^\[[0-9a-fA-F:]+\]:\d{1,5}$/.test(inviteAddress.trim()));

	function openInvite() {
		inviteAddress = '';
		inviteOpen = true;
	}

	async function invite(nodeId: string, address: string) {
		inviting = nodeId || address;
		try {
			const res = await rpc.mesh.invite({ nodeId, address });
			toast.success('Invited', res.admission?.node?.name || address);
			if (address) inviteOpen = false;
		} catch (err) {
			toast.error('Invite failed', reason(err));
		} finally {
			inviting = undefined;
		}
	}

	async function decide(action: 'admit' | 'refuse' | 'dismiss', a: Admission) {
		try {
			if (action === 'admit') await rpc.mesh.admit({ admissionId: a.id });
			else if (action === 'refuse') await rpc.mesh.refuse({ admissionId: a.id });
			else await rpc.mesh.dismiss({ admissionId: a.id });
		} catch (err) {
			toast.error(`${action === 'admit' ? 'Admit' : action === 'refuse' ? 'Refuse' : 'Dismiss'} failed`, reason(err));
		}
	}

	function deciding(a: Admission): boolean {
		return a.state === AdmissionState.PENDING && a.side === AdmissionSide.MEMBER;
	}
</script>

{#snippet inviteButton()}
	<NeoButton class="primary" elevation={2} onclick={openInvite}>Invite</NeoButton>
{/snippet}

{#snippet nodesCard()}
	<Card title="Nearby nodes" tools={inviteButton} flush>
		<List head={{ primary: 'Node', secondary: 'Address', facts: ['Version', 'Mesh'] }}>
			{#each nodes as n (n.id)}
				<ListRow
					primary={n.name}
					primaryTip={[`${n.os} ${n.arch}`.trim(), `heard ${when(n.heardAt)}`, n.from ? `from ${n.from}` : ''].filter(Boolean).join('\n')}
					secondary={n.address || n.from}
					secondaryKind="address"
					secondaryWidth="24ch"
					facts={[{ value: n.version, mono: true }, { value: membership(n) }]}
				>
					{#snippet action()}
						{#if outside(n)}<RowAction loading={inviting === n.id} onclick={() => invite(n.id, '')} label="Invite" />{/if}
					{/snippet}
				</ListRow>
			{/each}
		</List>
	</Card>
{/snippet}

{#snippet meshesCard()}
	<Card title="Nearby meshes" tools={nodes.length ? undefined : inviteButton} flush>
		<List head={{ primary: 'Mesh', secondary: 'Members' }}>
			{#each meshes as m (m.hash)}
				<ListRow primary={m.name} primaryTip="heard {when(m.heardAt)}" secondary={members(m.members)} secondaryWidth="12ch" />
			{/each}
		</List>
	</Card>
{/snippet}

{#snippet admissionsCard()}
	<Card title="Admissions" tools={heard ? undefined : inviteButton} flush>
		<List head={{ primary: 'Node', secondary: 'Side', facts: ['Expires'] }}>
			{#each admissions as a (a.id)}
				<ListRow
					primary={a.node?.name || a.node?.address || a.meshName}
					primaryTip={admissionTip(a)}
					secondary={sideWord(a)}
					secondaryWidth="10ch"
					facts={[a.expiresAt && !settled(a) ? { value: ago(a.expiresAt, clockTick.now), tip: when(a.expiresAt) } : { value: '' }]}
					menu={deciding(a) ? [{ label: 'Refuse', danger: true, confirm: `Refuse ${a.node?.name || 'this node'}`, onclick: () => decide('refuse', a) }] : undefined}
				>
					{#snippet badges()}<StatePill label={enumLabel(AdmissionStateSchema, a.state)} lamp={admissionLamp(a.state)} />{/snippet}
					{#snippet action()}
						{#if deciding(a) && a.asked}
							<RowAction onclick={() => decide('admit', a)} label="Admit" />
						{:else if settled(a)}
							<RowAction onclick={() => decide('dismiss', a)} label="Dismiss" />
						{/if}
					{/snippet}
				</ListRow>
			{/each}
		</List>
	</Card>
{/snippet}

{#if !heard && admissions.length === 0}
	<Card title="Nearby" tools={inviteButton}>
		<Empty icon={BroadcastIcon} text="Nothing nearby" />
	</Card>
{:else if heard && admissions.length}
	<Columns ratio="3/2">
		{#snippet left()}
			{#if nodes.length}{@render nodesCard()}{/if}
			{#if meshes.length}{@render meshesCard()}{/if}
		{/snippet}
		{#snippet right()}{@render admissionsCard()}{/snippet}
	</Columns>
{:else}
	{#if nodes.length}{@render nodesCard()}{/if}
	{#if meshes.length}{@render meshesCard()}{/if}
	{#if admissions.length}{@render admissionsCard()}{/if}
{/if}

<Dialog bind:open={inviteOpen} title="Invite by address" confirm="Invite" busy={inviting === inviteAddress.trim()} disabled={!inviteValid} onconfirm={() => invite('', inviteAddress.trim())}>
	<Text bind:value={inviteAddress} label="Address" info="host:port" mono required error={inviteAddress.trim() && !inviteValid ? 'Host and port' : undefined} />
</Dialog>
