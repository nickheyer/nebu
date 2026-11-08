<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { enumLabel, micros, num, plural, tokensPerSecond } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { ShapeSchema } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { NodeState, NodeStateSchema, type Formation, type MeshStatus, type Node } from '#lib/proto/nebu/v1/mesh_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LogView from '#lib/ui/LogView.svelte';
	import NameConfirm from '#lib/ui/NameConfirm.svelte';
	import Pop from '#lib/ui/Pop.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import { formationLamp, nodeLamp } from '#lib/ui/status.js';
	import Federation from '#lib/pages/mesh/Federation.svelte';
	import FormationPanel from '#lib/pages/mesh/FormationPanel.svelte';
	import MeshCard from '#lib/pages/mesh/MeshCard.svelte';
	import Nearby from '#lib/pages/mesh/Nearby.svelte';
	import NodePanel from '#lib/pages/mesh/NodePanel.svelte';
	import Outside from '#lib/pages/mesh/Outside.svelte';
	import Tuning from '#lib/pages/mesh/Tuning.svelte';
	import { deviceCount, formationTip, isMeshTab, linkLabel, linkLamp, meshHeaderTabs, nodeShape, nodeTip, peerName, planFormationHref, worstLink, type MeshTab } from '#lib/pages/mesh/mesh.js';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import StackIcon from 'phosphor-svelte/lib/StackIcon';

	let { tab = 'nodes', id = '' }: { tab?: string; id?: string } = $props();

	let discovered = $state<MeshStatus | undefined>(undefined);
	const active = $derived<MeshTab>(isMeshTab(tab) ? tab : 'nodes');
	const status = $derived(live.mesh ?? discovered);
	const mesh = $derived(status?.mesh);
	const self = $derived([...live.nodes.values()].find((n) => n.self));
	const nodes = $derived([...live.nodes.values()].sort((a, b) => Number(b.self) - Number(a.self) || a.name.localeCompare(b.name)));
	const formations = $derived([...live.formations.values()].sort((a, b) => a.name.localeCompare(b.name)));
	const federations = $derived(status?.federations ?? []);
	const infos: Record<MeshTab, string> = {
		nodes: 'Every machine in the mesh with what it carries',
		links: 'How fast each pair of nodes reaches the other, measured on demand',
		formations: 'Models spread across nodes, each seat holding its share',
		nearby: 'Nodes and meshes heard on the network and the admissions this mesh decides',
		federation: 'Other meshes trading pools with this one',
		tuning: 'The speeds the planner assumes and the ratios it has learned'
	};

	onMount(async () => {
		try {
			discovered = (await rpc.mesh.discover({})).status;
		} catch (err) {
			toast.error('Mesh failed to load', reason(err));
		}
	});

	function close() {
		void goto(`/mesh/${active}`);
	}

	// Nodes
	const openNode = $derived((active === 'nodes' || active === 'links') && id ? live.nodes.get(id) : undefined);
	let forgetting = $state<Node | undefined>(undefined);
	let forgetOpen = $state(false);

	function askForget(n: Node) {
		forgetting = n;
		forgetOpen = true;
	}

	async function forget() {
		if (!forgetting) return;
		try {
			await rpc.mesh.forgetNode({ nodeId: forgetting.id });
			toast.success(`Forgot ${forgetting.name}`);
			if (id === forgetting.id) close();
		} catch (err) {
			toast.error('Forget failed', reason(err));
		}
	}

	// Links
	let probeForce = $state(false);
	let probing = $state<string | undefined>(undefined);

	// Measures links to one member, or every member when the id is empty
	async function probe(nodeId: string, done?: () => void) {
		probing = nodeId;
		try {
			const res = await rpc.mesh.probe({ nodeId, force: probeForce });
			toast.success('Probed', plural(res.links.length, 'link'));
			done?.();
		} catch (err) {
			toast.error('Probe failed', reason(err));
		} finally {
			probing = undefined;
		}
	}

	// Formations
	let fetchedFormation = $state<Formation | undefined>(undefined);
	let formationError = $state('');
	let formationLogs = $state(false);
	const openFormation = $derived(active === 'formations' && id ? (live.formations.get(id) ?? (fetchedFormation?.id === id || fetchedFormation?.name === id ? fetchedFormation : undefined)) : undefined);

	// Fetches an open formation the stream has not carried yet
	$effect(() => {
		const want = active === 'formations' ? id : '';
		formationError = '';
		if (!want || live.formations.has(want)) return;
		const ac = new AbortController();
		rpc.mesh
			.getFormation({ id: want }, { signal: ac.signal })
			.then((res) => (fetchedFormation = res.formation))
			.catch((err) => {
				if (!ac.signal.aborted) formationError = reason(err);
			});
		return () => ac.abort();
	});

	function showFormation(f: Formation, logs = false) {
		formationLogs = logs;
		void goto(`/mesh/formations/${encodeURIComponent(f.id)}`);
	}

	function closeFormation() {
		formationLogs = false;
		close();
	}

	async function stopFormation(f: Formation) {
		try {
			await rpc.mesh.stopFormation({ id: f.id });
			toast.success(`Stopping ${f.name}`);
		} catch (err) {
			toast.error('Stop failed', reason(err));
		}
	}

	async function deleteFormation(f: Formation) {
		try {
			await rpc.mesh.deleteFormation({ id: f.id });
			toast.success(`Deleted ${f.name}`);
			if (id === f.id) closeFormation();
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}

	// Streams the open formation's seat output, each line prefixed with its seat
	function formationLogSource(tail: number, follow: boolean, signal: AbortSignal) {
		const formationId = openFormation?.id ?? id;
		return (async function* () {
			for await (const res of rpc.mesh.formationLogs({ id: formationId, tail, follow }, { signal })) {
				const seat = [res.nodeId ? peerName(live, res.nodeId) : '', res.role].filter(Boolean).join(' ');
				yield seat ? res.lines.map((l) => `${seat} ${l}`) : res.lines;
			}
		})();
	}
</script>

{#snippet nodeBadges(n: Node)}
	{#if n.self}<StatePill label="you" />{/if}
	{#if n.issuer}<StatePill label="issuer" lamp="primary" />{/if}
	{#if n.state !== NodeState.READY}<StatePill label={enumLabel(NodeStateSchema, n.state)} lamp={nodeLamp(n.state)} />{/if}
{/snippet}

{#snippet nodesList()}
	<Card title="Members" flush>
		<List head={{ primary: 'Node', facts: ['Version', 'Devices'] }}>
			{#each nodes as n (n.id)}
				<ListRow
					lamp={nodeLamp(n.state)}
					shape={nodeShape(n.state)}
					primary={n.name}
					primaryTip={nodeTip(n)}
					facts={[{ value: n.version, mono: true }, { value: deviceCount(n) }]}
					selected={n.id === id}
					onclick={() => goto(`/mesh/nodes/${encodeURIComponent(n.id)}`)}
					menu={n.self ? undefined : [{ label: 'Forget', danger: true, onclick: () => askForget(n) }]}
				>
					{#snippet badges()}{@render nodeBadges(n)}{/snippet}
				</ListRow>
			{/each}
		</List>
	</Card>
{/snippet}

{#snippet linksList()}
	<Card title="Links" flush>
		{#snippet tools()}
			<Pop placement="bottom-end" width="20rem">
				{#snippet content(closePop)}
					<Switch bind:checked={probeForce} label="Force" />
					<Row gap={12} justify="flex-end">
						<NeoButton class="primary" elevation={2} loading={probing === ''} onclick={() => probe('', closePop)}>Probe all</NeoButton>
					</Row>
				{/snippet}
				<NeoButton elevation={2}>Probe all</NeoButton>
			</Pop>
		{/snippet}
		<List head={{ primary: 'Node', facts: ['Measured', 'Slowest RTT'] }}>
			{#each nodes as n (n.id)}
				{@const worst = worstLink(n)}
				<ListRow
					lamp={nodeLamp(n.state)}
					shape={nodeShape(n.state)}
					primary={n.name}
					primaryTip={nodeTip(n)}
					facts={[{ value: n.links.length ? num(n.links.length) : '' }, { value: worst ? micros(worst.rttUs) : '' }]}
					selected={n.id === id}
					onclick={() => goto(`/mesh/links/${encodeURIComponent(n.id)}`)}
				>
					{#snippet badges()}{#if worst}<StatePill label={linkLabel(worst.class)} lamp={linkLamp(worst.class)} />{/if}{/snippet}
					{#snippet action()}
						{#if !n.self}<RowAction loading={probing === n.id} onclick={() => probe(n.id)} label="Probe" />{/if}
					{/snippet}
				</ListRow>
			{/each}
		</List>
	</Card>
{/snippet}

{#snippet formationsList()}
	<Card title="Formations" flush>
		{#snippet tools()}
			<NeoButton class="primary" elevation={2} href={planFormationHref()}>{#snippet icon()}<PlusIcon size={16} />{/snippet}Formation</NeoButton>
		{/snippet}
		{#if formations.length === 0}
			<Empty icon={StackIcon} text="No formations" action="Plan one" href={planFormationHref()} />
		{:else}
			<List head={{ primary: 'Formation', secondary: 'Shape', facts: ['Nodes', 'Speed'] }}>
				{#each formations as f (f.id)}
					<ListRow
						lamp={formationLamp(f.state)}
						primary={f.name}
						primaryTip={formationTip(f)}
						secondary={enumLabel(ShapeSchema, f.shape)}
						secondaryWidth="10ch"
						facts={[{ value: num(f.seats.length) }, { value: f.plan ? tokensPerSecond(f.plan.tokensPerSecond) : '' }]}
						selected={f.id === id}
						onclick={() => showFormation(f)}
						menu={[
							{ label: 'Logs', onclick: () => showFormation(f, true) },
							{ label: 'Stop', confirm: `Stop ${f.name}`, disabled: !f.desiredRunning, onclick: () => stopFormation(f), divider: true },
							{ label: 'Delete', danger: true, confirm: `Delete ${f.name}`, onclick: () => deleteFormation(f) }
						]}
					/>
				{/each}
			</List>
		{/if}
	</Card>
{/snippet}

{#snippet missing(title: string, text: string, hint?: string)}
	<PanelHeader {title} onclose={close} />
	<Empty {text} {hint} />
{/snippet}

{#snippet meshCard()}
	{#if mesh}<MeshCard {mesh} {self} />{/if}
{/snippet}

{#if !live.ready && !discovered}
	<PageHeader />
	<Card flush><Skeleton rows={4} secondary={false} facts={2} /></Card>
{:else if !mesh}
	<Outside {status} />
{:else}
	<PageHeader tabs={meshHeaderTabs} {active} info={infos[active]} />
	{#if active === 'nodes' || active === 'links'}
		<Sheet open={id !== ''} onclose={close} head={meshCard} list={active === 'nodes' ? nodesList : linksList}>
			{#if openNode}
				{#key `${active}/${openNode.id}`}<NodePanel node={openNode} tab={active === 'links' ? 'links' : 'profile'} onclose={close} />{/key}
			{:else if !live.ready}
				<Skeleton rows={6} kind="facts" />
			{:else}
				{@render missing('Node', 'No such node')}
			{/if}
		</Sheet>
	{:else if active === 'formations'}
		<Sheet open={id !== ''} onclose={closeFormation} head={meshCard} list={formationsList} width={formationLogs ? 720 : 560}>
			{#if openFormation}
				{#key openFormation.id}
					{#if formationLogs}
						<PanelHeader title={openFormation.name} subtitle="logs" onclose={closeFormation} />
						<LogView source={formationLogSource} name={openFormation.name} levels height="70vh" />
					{:else}
						<FormationPanel formation={openFormation} onclose={closeFormation} />
					{/if}
				{/key}
			{:else if formationError}
				{@render missing('Formation', 'Formation failed to load', formationError)}
			{:else}
				<Skeleton rows={6} kind="facts" />
			{/if}
		</Sheet>
	{:else if active === 'nearby'}
		{@render meshCard()}
		<Nearby {status} />
	{:else if active === 'federation'}
		<Federation {federations} {id} head={meshCard} />
	{:else}
		<Tuning head={meshCard} />
	{/if}
{/if}

{#if forgetting}
	<NameConfirm bind:open={forgetOpen} title="Forget node" name={forgetting.name} message="Revokes its certificate everywhere" confirm="Forget" onconfirm={forget} />
{/if}
