<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { daysUntil, num, thin, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { NodeState, type Mesh, type Node } from '#lib/proto/nebu/v1/mesh_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Facts, { type FactItem } from '#lib/ui/Facts.svelte';
	import Menu from '#lib/ui/Menu.svelte';
	import NameConfirm from '#lib/ui/NameConfirm.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { tokenTtlMax } from '#lib/pages/mesh/mesh.js';

	let { mesh, self }: { mesh: Mesh; self: Node | undefined } = $props();

	type Danger = 'rotate' | 'handoff' | 'reset';
	const dangers: Record<Danger, { title: string; confirm: string; message: string }> = {
		rotate: { title: 'Rotate authority', confirm: 'Rotate', message: 'Reissues every reachable certificate' },
		handoff: { title: 'Hand off issuer', confirm: 'Hand off', message: 'Moves the authority key' },
		reset: { title: 'Reset mesh', confirm: 'Reset', message: 'Clears the mesh and its formations' }
	};

	const certDays = $derived(daysUntil(mesh.certificateExpiresAt, clockTick.now));
	let ttl = $state(3600);
	let tokenOpen = $state(false);
	let copying = $state(false);
	let danger = $state<Danger | undefined>(undefined);
	let dangerOpen = $state(false);
	let handoffTo = $state<string | undefined>(undefined);

	const items = $derived.by((): FactItem[] => [
		{ id: 'members', label: 'Members', value: num(mesh.members) },
		{ id: 'you', label: 'You', value: self?.name ?? '', tip: self?.issuer ? 'issuer' : undefined, lamp: self?.issuer ? 'primary' : undefined },
		{ id: 'cert', label: 'Certificate', value: certDays === undefined ? '' : `${certDays}${thin}d`, tip: when(mesh.certificateExpiresAt) || undefined, lamp: certDays !== undefined && certDays < 30 ? 'warning' : undefined },
		{ id: 'address', label: 'Address', value: mesh.address, mono: true, tip: mesh.address },
		{ id: 'exposure', label: 'Exposure', value: mesh.exposure },
		{ id: 'announce', label: 'Announce', value: mesh.announce ? 'on' : 'off' },
		{ id: 'ca', label: 'CA', value: mesh.caFingerprint.slice(0, 12), mono: true, tip: mesh.caFingerprint },
		{ id: 'id', label: 'Id', value: mesh.id.slice(0, 12), mono: true, tip: mesh.id }
	]);

	// Mints a join token for the chosen lifetime and puts it on the clipboard
	async function copyToken() {
		copying = true;
		try {
			const res = await rpc.mesh.token({ ttlSeconds: ttl });
			await navigator.clipboard.writeText(res.token);
			toast.success('Join token copied', `expires ${when(res.expiresAt)}`);
			tokenOpen = false;
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

<Card title={mesh.name}>
	{#snippet tools()}
		<NeoButton elevation={2} onclick={() => (tokenOpen = true)}>Token</NeoButton>
		<Confirm label="Leave" question="Leave {mesh.name}" danger small={false} onconfirm={leave} />
		<Menu
			items={[
				{ label: 'Rotate authority', onclick: () => ask('rotate') },
				{ label: 'Hand off issuer', onclick: () => ask('handoff'), disabled: !self?.issuer, description: self?.issuer ? undefined : 'issuer only' },
				{ label: 'Reset mesh', onclick: () => ask('reset'), danger: true, divider: true }
			]}
		/>
	{/snippet}
	<Facts {items} />
</Card>

<Dialog bind:open={tokenOpen} title="Join token" confirm="Copy" busy={copying} disabled={ttl <= 0} onconfirm={copyToken}>
	<Duration bind:value={ttl} base="s" max={tokenTtlMax} label="Valid for" />
</Dialog>

{#if danger}
	<NameConfirm bind:open={dangerOpen} title={dangers[danger].title} name={mesh.name} message={dangers[danger].message} confirm={dangers[danger].confirm} enabled={danger !== 'handoff' || handoffTo !== undefined} onconfirm={run}>
		{#if danger === 'handoff'}
			<Picker bind:value={handoffTo} fetch={fetchNodes} label="New issuer" info="Signs new members from now on" required current={handoffTo ? live.nodes.get(handoffTo)?.name : undefined} />
		{/if}
	</NameConfirm>
{/if}
