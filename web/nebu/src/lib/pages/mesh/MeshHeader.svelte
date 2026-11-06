<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { daysUntil, thin, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { NodeState, type Mesh, type Node } from '#lib/proto/nebu/v1/mesh_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import InfoTip from '#lib/ui/InfoTip.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import Menu from '#lib/ui/Menu.svelte';
	import NameConfirm from '#lib/ui/NameConfirm.svelte';
	import Pop from '#lib/ui/Pop.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { members, tokenTtlMax } from '#lib/pages/mesh/mesh.js';

	let { mesh, self }: { mesh: Mesh; self: Node | undefined } = $props();

	type Danger = 'rotate' | 'handoff' | 'reset';
	const dangers: Record<Danger, { title: string; confirm: string; message: string }> = {
		rotate: { title: 'Rotate authority', confirm: 'Rotate', message: 'New certificates for reachable members, the rest join again' },
		handoff: { title: 'Hand off issuer', confirm: 'Hand off', message: 'Issuer role and authority key move to the chosen member' },
		reset: { title: 'Reset mesh', confirm: 'Reset', message: 'Leaves and clears the mesh, formations included' }
	};

	const certDays = $derived(daysUntil(mesh.certificateExpiresAt, clockTick.now));
	let ttl = $state(3600);
	let copying = $state(false);
	let danger = $state<Danger | undefined>(undefined);
	let dangerOpen = $state(false);
	let handoffTo = $state<string | undefined>(undefined);

	// Mints a join token for the chosen lifetime and puts it on the clipboard
	async function copyToken(close: () => void) {
		copying = true;
		try {
			const res = await rpc.mesh.token({ ttlSeconds: ttl });
			await navigator.clipboard.writeText(res.token);
			toast.success('Join token copied', `expires ${when(res.expiresAt)}`);
			close();
		} catch (err) {
			toast.error('Join token failed', reason(err));
		} finally {
			copying = false;
		}
	}

	async function leave() {
		try {
			await rpc.mesh.leave({});
			toast.success(`Left ${mesh.name}`);
		} catch (err) {
			toast.error('Leave failed', reason(err));
		}
	}

	function ask(d: Danger) {
		danger = d;
		handoffTo = undefined;
		dangerOpen = true;
	}

	// Ready members other than this node, searched by the daemon as typed
	async function fetchNodes(query: string, cursor: string, limit: number, signal: AbortSignal) {
		const res = await rpc.mesh.listNodes({ query, cursor, limit, state: NodeState.READY }, { signal });
		return { items: res.nodes.map((n) => ({ value: n.id, label: n.name, description: n.self ? 'this node' : n.address, disabled: n.self })), next: res.nextCursor };
	}

	async function run() {
		try {
			if (danger === 'rotate') {
				const res = await rpc.mesh.rotate({});
				if (res.missed.length) toast.warning('Authority rotated', `${res.rotated.length} reissued, missed ${res.missed.map((id) => live.nodes.get(id)?.name ?? id).join(', ')}`);
				else toast.success('Authority rotated', `${res.rotated.length} reissued`);
			} else if (danger === 'handoff') {
				await rpc.mesh.handOffIssuer({ nodeId: handoffTo ?? '' });
				toast.success('Issuer handed off', live.nodes.get(handoffTo ?? '')?.name);
			} else if (danger === 'reset') {
				await rpc.mesh.resetMesh({});
				toast.success('Mesh reset');
			}
		} catch (err) {
			toast.error(`${danger ? dangers[danger].title : 'Mesh change'} failed`, reason(err));
		}
	}
</script>

{#snippet details()}
	<KeyRows
		dense
		rows={[
			{ label: 'id', value: mesh.id, mono: true, tip: mesh.id },
			{ label: 'address', value: mesh.address || 'loopback only', mono: true },
			{ label: 'exposure', value: mesh.exposure || 'unset' },
			{ label: 'announce', value: mesh.announce ? 'on' : 'off' },
			{ label: 'CA fingerprint', value: mesh.caFingerprint, mono: true, copy: true, tip: mesh.caFingerprint }
		]}
	/>
{/snippet}

{#snippet title()}
	<h1 class="title"><Str value={mesh.name} weight={600} width="100%" /></h1>
{/snippet}

{#snippet standing()}
	<Cell grow={false}><span class="secondary nowrap">{members(mesh.members)}</span></Cell>
	{#if self}
		<Cell grow={false}>
			<Row gap={4} wrap={false}>
				<Str value={self.name} />
				<StatePill label="you" />
				{#if self.issuer}<StatePill label="issuer" lamp="primary" />{/if}
			</Row>
		</Cell>
	{/if}
	{#if certDays !== undefined}
		<Cell grow={false}>
			<Tip text="certificate expires {when(mesh.certificateExpiresAt)}" tag="span">
				<span class="secondary nowrap" class:warning-text={certDays < 30}>cert {certDays}{thin}d</span>
			</Tip>
		</Cell>
	{/if}
	<Cell grow={false}><InfoTip click content={details} /></Cell>
{/snippet}

{#snippet actions()}
	<Cell grow={false} push>
		<Row gap={8} wrap={false}>
			<Pop placement="bottom-end" width="20rem">
				{#snippet content(close)}
					<Stack gap={12}>
						<Duration bind:value={ttl} base="s" max={tokenTtlMax} label="TTL" />
						<Row gap={12} justify="flex-end">
							<NeoButton class="primary" elevation={2} loading={copying} disabled={ttl <= 0} onclick={() => copyToken(close)}>Copy</NeoButton>
						</Row>
					</Stack>
				{/snippet}
				<NeoButton elevation={2}>Token</NeoButton>
			</Pop>
			<Confirm label="Leave" question="Leave {mesh.name}" danger small={false} onconfirm={leave} />
			<Menu
				items={[
					{ label: 'Rotate authority', onclick: () => ask('rotate') },
					{ label: 'Hand off issuer', onclick: () => ask('handoff'), disabled: !self?.issuer, description: self?.issuer ? undefined : 'issuer only' },
					{ label: 'Reset mesh', onclick: () => ask('reset'), danger: true, divider: true }
				]}
			/>
		</Row>
	</Cell>
{/snippet}

{#if viewport.mobile}
	<Stack gap={8}>
		<Row gap={12} wrap={false}>
			<Cell min="6rem" max="24ch">{@render title()}</Cell>
			{@render actions()}
		</Row>
		<Row gap={12}>{@render standing()}</Row>
	</Stack>
{:else}
	<Row gap={12}>
		<Cell grow={false} max="24ch">{@render title()}</Cell>
		{@render standing()}
		{@render actions()}
	</Row>
{/if}

{#if danger}
	<NameConfirm bind:open={dangerOpen} title={dangers[danger].title} name={mesh.name} message={dangers[danger].message} confirm={dangers[danger].confirm} enabled={danger !== 'handoff' || handoffTo !== undefined} onconfirm={run}>
		{#if danger === 'handoff'}
			<Picker bind:value={handoffTo} fetch={fetchNodes} label="New issuer" placeholder="Search nodes" required current={handoffTo ? live.nodes.get(handoffTo)?.name : undefined} />
		{/if}
	</NameConfirm>
{/if}

<style>
	.title {
		display: flex;
		min-width: 0;
	}
</style>
