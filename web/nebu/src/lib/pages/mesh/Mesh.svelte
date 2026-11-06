<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { create } from '@bufbuild/protobuf';
	import type { Timestamp } from '@bufbuild/protobuf/wkt';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoDialog } from '#lib/neo/floating/dialog/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ago, bandwidth, enumLabel, micros, num, plural, seconds, tokensPerSecond, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { ShapeSchema } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { AdmissionSide, AdmissionState, AdmissionStateSchema, DeviceProfileSchema, FederationRole, FederationRoleSchema, NodeState, NodeStateSchema, type Admission, type DeviceProfile, type Formation, type FormationRatio, type MeshStatus, type Node } from '#lib/proto/nebu/v1/mesh_pb.js';
	import type { Pool } from '#lib/proto/nebu/v1/pool_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Confirm from '#lib/ui/Confirm.svelte';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LogView from '#lib/ui/LogView.svelte';
	import NameConfirm from '#lib/ui/NameConfirm.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import Pop from '#lib/ui/Pop.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Time from '#lib/ui/Time.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import Split from '#lib/ui/layout/Split.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { admissionLamp, formationLamp, nodeLamp } from '#lib/ui/status.js';
	import { exported, poolLamp, poolShape, poolWord } from '#lib/pages/gateway/gateway.js';
	import FormationPanel from '#lib/pages/mesh/FormationPanel.svelte';
	import MeshHeader from '#lib/pages/mesh/MeshHeader.svelte';
	import NodePanel from '#lib/pages/mesh/NodePanel.svelte';
	import Outside from '#lib/pages/mesh/Outside.svelte';
	import { admissionTip, deviceCount, flopsRate, formationTip, isMeshTab, linkLabel, linkLamp, linkTip, members, membership, nodeShape, nodeTip, outside, peerName, planFormationHref, settled, sideWord, streamRate, tokenTtlMax, worstLink, type MeshTab } from '#lib/pages/mesh/mesh.js';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';

	let { tab = 'nodes', id = '' }: { tab?: string; id?: string } = $props();

	let discovered = $state<MeshStatus | undefined>(undefined);
	const active = $derived<MeshTab>(isMeshTab(tab) ? tab : 'nodes');
	const status = $derived(live.mesh ?? discovered);
	const mesh = $derived(status?.mesh);
	const self = $derived([...live.nodes.values()].find((n) => n.self));
	const nodes = $derived([...live.nodes.values()].sort((a, b) => Number(b.self) - Number(a.self) || a.name.localeCompare(b.name)));
	const formations = $derived([...live.formations.values()].sort((a, b) => a.name.localeCompare(b.name)));
	const admissions = $derived(status?.admissions ?? []);
	const nearbyNodes = $derived(status?.nodes ?? []);
	const nearbyMeshes = $derived(status?.meshes ?? []);
	const federations = $derived(status?.federations ?? []);
	const tabs = $derived([
		{ id: 'nodes', label: 'Nodes', count: nodes.length },
		{ id: 'links', label: 'Links' },
		{ id: 'formations', label: 'Formations', count: formations.length },
		{ id: 'nearby', label: 'Nearby', count: admissions.filter((a) => !settled(a)).length || undefined },
		{ id: 'federation', label: 'Federation', count: federations.length || undefined },
		{ id: 'tuning', label: 'Tuning' }
	]);

	// The mesh status the daemon answers before the stream's snapshot lands
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

	// Create a mesh, then show one join token with its expiry
	let createOpen = $state(false);
	let meshName = $state('');
	let made = $state<{ token: string; expires: Timestamp | undefined } | undefined>(undefined);
	let joinOpen = $state(false);
	let joinToken = $state('');
	let busy = $state(false);

	function openCreate() {
		meshName = '';
		made = undefined;
		createOpen = true;
	}

	async function createMesh() {
		busy = true;
		try {
			await rpc.mesh.init({ name: meshName.trim() });
			toast.success(`Created ${meshName.trim()}`);
		} catch (err) {
			toast.error('Create failed', reason(err));
			busy = false;
			return;
		}
		try {
			const res = await rpc.mesh.token({ ttlSeconds: 0 });
			made = { token: res.token, expires: res.expiresAt };
		} catch (err) {
			toast.error('Join token failed', reason(err));
			createOpen = false;
		} finally {
			busy = false;
		}
	}

	async function join() {
		busy = true;
		try {
			const res = await rpc.mesh.join({ token: joinToken.trim() });
			toast.success(`Joined ${res.mesh?.name ?? 'the mesh'}`);
			joinOpen = false;
			joinToken = '';
		} catch (err) {
			toast.error('Join failed', reason(err));
		} finally {
			busy = false;
		}
	}

	// Nodes
	const selectedNode = $derived(active === 'nodes' && id ? live.nodes.get(id) : undefined);
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
	let linkNodeId = $state('');
	let linkOpen = $state(false);
	let probeForce = $state(false);
	let probing = $state<string | undefined>(undefined);
	const linkNode = $derived(linkNodeId ? live.nodes.get(linkNodeId) : undefined);

	function openLinks(n: Node) {
		linkNodeId = n.id;
		linkOpen = true;
	}

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
	const selectedFormation = $derived(active === 'formations' && id ? (live.formations.get(id) ?? (fetchedFormation?.id === id || fetchedFormation?.name === id ? fetchedFormation : undefined)) : undefined);

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

	function openFormation(f: Formation, logs = false) {
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

	// Streams the open formation's seat output, each line prefixed with its node and role
	function formationLogSource(tail: number, follow: boolean, signal: AbortSignal) {
		const formationId = selectedFormation?.id ?? id;
		return (async function* () {
			for await (const res of rpc.mesh.formationLogs({ id: formationId, tail, follow }, { signal })) {
				const seat = [res.nodeId ? peerName(live, res.nodeId) : '', res.role].filter(Boolean).join(' ');
				yield seat ? res.lines.map((l) => `${seat} ${l}`) : res.lines;
			}
		})();
	}

	// Nearby
	let inviteAddress = $state('');
	let inviting = $state<string | undefined>(undefined);
	const inviteValid = $derived(/^[^\s:[\]]+:\d{1,5}$|^\[[0-9a-fA-F:]+\]:\d{1,5}$/.test(inviteAddress.trim()));

	async function invite(nodeId: string, address: string) {
		inviting = nodeId || address;
		try {
			const res = await rpc.mesh.invite({ nodeId, address });
			toast.success('Invited', res.admission?.node?.name || address);
			if (address) inviteAddress = '';
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

	// Federation
	let exportTtl = $state(86_400);
	let exporting = $state(false);
	let importToken = $state('');
	let importing = $state(false);
	let fedId = $state('');
	let fedOpen = $state(false);
	const fedNode = $derived(federations.find((n) => n.id === fedId));

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
			importToken = '';
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
			if (fedId === n.id) fedOpen = false;
		} catch (err) {
			toast.error('Drop failed', reason(err));
		}
	}

	// Pools the federation moves, the exporter's imported here or this node's exported to the importer
	function fedPools(n: Node): { pool: Pool; href: string }[] {
		if (n.federation?.role === FederationRole.EXPORTER) return n.pools.map((p) => ({ pool: p, href: `/gateway/imports/${encodeURIComponent(p.id)}` }));
		return [...live.pools.values()].filter(exported).map((p) => ({ pool: p, href: `/gateway/pools/${encodeURIComponent(p.id)}` }));
	}

	// Tuning
	let profiles = $state<DeviceProfile[]>([]);
	let ratios = $state<FormationRatio[]>([]);
	let tablesLoaded = $state(false);
	let editorOpen = $state(false);
	let editingPattern = $state<string | undefined>(undefined);
	let pattern = $state('');
	let streamGb = $state(0);
	let tflops = $state(0);
	let fixedSeconds = $state(0);
	let start = $state({ pattern: '', streamGb: 0, tflops: 0, fixedSeconds: 0 });
	let profileSaving = $state(false);
	let profileError = $state<string | undefined>(undefined);
	const profileDirty = $derived(editingPattern === undefined || pattern !== start.pattern || streamGb !== start.streamGb || tflops !== start.tflops || fixedSeconds !== start.fixedSeconds);

	async function loadTables() {
		try {
			const res = await rpc.mesh.getMesh({});
			profiles = res.profiles;
			ratios = res.ratios;
		} catch (err) {
			toast.error('Tuning failed to load', reason(err));
		} finally {
			tablesLoaded = true;
		}
	}

	// Loads the learned tables while the tuning tab shows, again after mesh changes
	$effect(() => {
		if (active !== 'tuning' || !mesh) return;
		void loadTables();
		let timer: ReturnType<typeof setTimeout> | undefined;
		const off = live.on(EventKind.MESH, () => {
			clearTimeout(timer);
			timer = setTimeout(() => void loadTables(), 1000);
		});
		return () => {
			clearTimeout(timer);
			off();
		};
	});

	function editProfile(p?: DeviceProfile) {
		editingPattern = p?.pattern;
		pattern = p?.pattern ?? '';
		streamGb = p ? p.streamBytesPerSecond / 1e9 : 0;
		tflops = p ? p.computeFlops / 1e12 : 0;
		fixedSeconds = p ? p.fixedSeconds : 0;
		start = { pattern, streamGb, tflops, fixedSeconds };
		profileError = undefined;
		editorOpen = true;
	}

	async function saveProfile() {
		profileSaving = true;
		profileError = undefined;
		try {
			const profile = create(DeviceProfileSchema, { pattern: pattern.trim(), streamBytesPerSecond: streamGb * 1e9, computeFlops: tflops * 1e12, fixedSeconds });
			profiles = (await rpc.mesh.setDeviceProfile({ profile })).profiles;
			toast.success(`Saved ${profile.pattern}`);
			editorOpen = false;
		} catch (err) {
			profileError = reason(err);
		} finally {
			profileSaving = false;
		}
	}

	async function deleteProfile(p: DeviceProfile) {
		try {
			profiles = (await rpc.mesh.deleteDeviceProfile({ pattern: p.pattern })).profiles;
			toast.success(`Deleted ${p.pattern}`);
			if (editingPattern === p.pattern) editorOpen = false;
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}

	function nodeBadgesShown(n: Node): boolean {
		return n.self || n.issuer || n.state !== NodeState.READY;
	}
</script>

{#snippet nodeBadges(n: Node)}
	{#if n.self}<StatePill label="you" />{/if}
	{#if n.issuer}<StatePill label="issuer" lamp="primary" />{/if}
	{#if n.state !== NodeState.READY}<StatePill label={enumLabel(NodeStateSchema, n.state)} lamp={nodeLamp(n.state)} />{/if}
{/snippet}

{#snippet nodesList()}
	<Stack gap={0}>
		{#each nodes as n (n.id)}
			{@const href = `/mesh/nodes/${encodeURIComponent(n.id)}`}
			<ListRow
				lamp={nodeLamp(n.state)}
				shape={nodeShape(n.state)}
				primary={n.name}
				primaryTip={nodeTip(n)}
				facts={[{ value: n.version, mono: true }, { value: deviceCount(n) }]}
				selected={n.id === id}
				onclick={() => goto(href)}
				menu={n.self ? undefined : [{ label: 'Forget', danger: true, onclick: () => askForget(n) }]}
			>
				{#snippet badges()}
					{#if !viewport.mobile}{@render nodeBadges(n)}{/if}
				{/snippet}
				{#snippet action()}<NeoButton text {href}>Open</NeoButton>{/snippet}
				{#snippet expand()}
					{#if viewport.mobile && nodeBadgesShown(n)}
						<Row gap={4}>{@render nodeBadges(n)}</Row>
					{/if}
					<Conditions conditions={n.conditions} />
				{/snippet}
			</ListRow>
		{:else}
			<Empty text="No members" />
		{/each}
	</Stack>
{/snippet}

{#snippet linksList()}
	<Stack gap={12}>
		<Row gap={12} justify="flex-end">
			<Pop placement="bottom-end" width="20rem">
				{#snippet content(closePop)}
					<Stack gap={12}>
						<Switch bind:checked={probeForce} label="Force" info="Measures bandwidth even while a formation serves" />
						<Row gap={12} justify="flex-end">
							<NeoButton class="primary" elevation={2} loading={probing === ''} onclick={() => probe('', closePop)}>Probe all</NeoButton>
						</Row>
					</Stack>
				{/snippet}
				<NeoButton elevation={2}>Probe all</NeoButton>
			</Pop>
		</Row>
		<Stack gap={0}>
			{#each nodes as n (n.id)}
				{@const worst = worstLink(n)}
				<ListRow lamp={nodeLamp(n.state)} shape={nodeShape(n.state)} primary={n.name} primaryTip={nodeTip(n)} selected={linkOpen && n.id === linkNodeId} onclick={() => openLinks(n)}>
					{#snippet badges()}<StatePill label={worst ? linkLabel(worst.class) : 'unmeasured'} lamp={worst ? linkLamp(worst.class) : 'muted'} />{/snippet}
					{#snippet action()}
						{#if !n.self}<NeoButton text loading={probing === n.id} onclick={() => probe(n.id)}>Probe</NeoButton>{/if}
					{/snippet}
					{#snippet expand()}
						<Stack gap={0}>
							{#each n.links as l (l.to)}
								<ListRow primary={peerName(live, l.to)} primaryTip={linkTip(l)} facts={[{ value: micros(l.rttUs) }, { value: bandwidth(l.streamBytesPerSecond) }]}>
									{#snippet badges()}<StatePill label={linkLabel(l.class)} lamp={linkLamp(l.class)} />{/snippet}
								</ListRow>
							{:else}
								<span class="secondary">No links measured</span>
							{/each}
						</Stack>
					{/snippet}
				</ListRow>
			{:else}
				<Empty text="No members" />
			{/each}
		</Stack>
	</Stack>
{/snippet}

{#snippet formationsList()}
	<Stack gap={12}>
		<Row gap={12}>
			<NeoButton elevation={2} href={planFormationHref()}>{#snippet icon()}<PlusIcon size={20} />{/snippet}Plan a formation</NeoButton>
		</Row>
		<Stack gap={0}>
			{#each formations as f (f.id)}
				{@const href = `/mesh/formations/${encodeURIComponent(f.id)}`}
				<ListRow
					lamp={formationLamp(f.state)}
					primary={f.name}
					primaryTip={formationTip(f)}
					secondary={enumLabel(ShapeSchema, f.shape)}
					secondaryWidth="10ch"
					facts={[{ value: plural(f.seats.length, 'node') }, { value: f.plan ? tokensPerSecond(f.plan.tokensPerSecond) : '' }]}
					selected={f.id === id}
					onclick={() => openFormation(f)}
					menu={[
						{ label: 'Stop', confirm: `Stop ${f.name}`, disabled: !f.desiredRunning, onclick: () => stopFormation(f) },
						{ label: 'Delete', danger: true, confirm: `Delete ${f.name}`, onclick: () => deleteFormation(f) },
						{ label: 'Logs', onclick: () => openFormation(f, true), divider: true }
					]}
				>
					{#snippet action()}<NeoButton text {href}>Open</NeoButton>{/snippet}
				</ListRow>
			{:else}
				<Empty text="No formations" />
			{/each}
		</Stack>
	</Stack>
{/snippet}

{#snippet nearbyNodesSection()}
	<Section title="Nodes" count={nearbyNodes.length} gap={0}>
		{#each nearbyNodes as n (n.id)}
			<ListRow
				primary={n.name}
				primaryTip={[`${n.os} ${n.arch}`.trim(), `heard ${when(n.heardAt)}`, n.from ? `from ${n.from}` : ''].filter(Boolean).join('\n')}
				secondary={n.address || n.from}
				secondaryKind="address"
				secondaryWidth="24ch"
				facts={[{ value: n.version, mono: true }, { value: membership(n) }]}
			>
				{#snippet action()}
					{#if outside(n)}<NeoButton text loading={inviting === n.id} onclick={() => invite(n.id, '')}>Invite</NeoButton>{/if}
				{/snippet}
			</ListRow>
		{:else}
			<Empty text="None heard" />
		{/each}
	</Section>
{/snippet}

{#snippet nearbyMeshesSection()}
	<Section title="Meshes" count={nearbyMeshes.length} gap={0}>
		{#each nearbyMeshes as m (m.hash)}
			<ListRow primary={m.name} primaryTip={`heard ${when(m.heardAt)}`} secondary={members(m.members)} secondaryWidth="12ch" />
		{:else}
			<Empty text="None heard" />
		{/each}
	</Section>
{/snippet}

{#snippet admissionsSection()}
	<Section title="Admissions" count={admissions.length} gap={0}>
		{#each admissions as a (a.id)}
			<ListRow
				primary={a.node?.name || a.node?.address || a.meshName}
				primaryTip={admissionTip(a)}
				secondary={sideWord(a)}
				secondaryWidth="10ch"
				facts={a.expiresAt && !settled(a) ? [{ value: `expires ${ago(a.expiresAt, clockTick.now)}`, tip: when(a.expiresAt) }] : []}
			>
				{#snippet badges()}<StatePill label={enumLabel(AdmissionStateSchema, a.state)} lamp={admissionLamp(a.state)} />{/snippet}
				{#snippet action()}
					{#if a.state === AdmissionState.PENDING && a.side === AdmissionSide.MEMBER}
						{#if a.asked}<NeoButton text onclick={() => decide('admit', a)}>Admit</NeoButton>{/if}
						<Confirm label="Refuse" question="Refuse {a.node?.name || 'this node'}" danger onconfirm={() => decide('refuse', a)} />
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

{#snippet inviteRow()}
	<Row gap={12} align="flex-start">
		<Cell min="12rem" max="24rem">
			<Text bind:value={inviteAddress} placeholder="Invite by address, host:port" mono error={inviteAddress.trim() && !inviteValid ? 'host and port' : undefined} />
		</Cell>
		<Cell grow={false}>
			<NeoButton elevation={2} loading={inviting === inviteAddress.trim()} disabled={!inviteValid} onclick={() => invite('', inviteAddress.trim())}>Invite</NeoButton>
		</Cell>
	</Row>
{/snippet}

{#snippet nearbyView()}
	{#if viewport.ultrawide}
		<Split ratio="3/5">
			{#snippet left()}
				{@render inviteRow()}
				{@render nearbyNodesSection()}
				{@render nearbyMeshesSection()}
			{/snippet}
			{#snippet right()}{@render admissionsSection()}{/snippet}
		</Split>
	{:else}
		<Stack gap={24}>
			{@render inviteRow()}
			{@render nearbyNodesSection()}
			{@render nearbyMeshesSection()}
			{@render admissionsSection()}
		</Stack>
	{/if}
{/snippet}

{#snippet federationList()}
	<Stack gap={12}>
		<Row gap={12} align="flex-start">
			<Cell grow={false}>
				<Pop placement="bottom-start" width="20rem">
					{#snippet content(closePop)}
						<Stack gap={12}>
							<Duration bind:value={exportTtl} base="s" max={tokenTtlMax} label="TTL" />
							<Row gap={12} justify="flex-end">
								<NeoButton class="primary" elevation={2} loading={exporting} disabled={exportTtl <= 0} onclick={() => copyExport(closePop)}>Copy</NeoButton>
							</Row>
						</Stack>
					{/snippet}
					<NeoButton elevation={2}>Export token</NeoButton>
				</Pop>
			</Cell>
			<Cell min="12rem" max="32rem" push><Text bind:value={importToken} placeholder="Import token" mono /></Cell>
			<Cell grow={false}><NeoButton elevation={2} loading={importing} disabled={!importToken.trim()} onclick={importMesh}>Import</NeoButton></Cell>
		</Row>
		<Stack gap={0}>
			{#each federations as n (n.id)}
				{@const fed = n.federation}
				<ListRow
					primary={fed?.meshName || n.name}
					primaryTip={`fingerprint ${fed?.fingerprint || 'none'}\nhash ${fed?.meshHash || 'none'}`}
					secondary={fed?.address || n.address}
					secondaryKind="address"
					secondaryWidth="24ch"
					facts={fed?.since ? [{ value: `since ${ago(fed.since, clockTick.now)}`, tip: when(fed.since) }] : []}
					selected={fedOpen && fedId === n.id}
					onclick={() => {
						fedId = n.id;
						fedOpen = true;
					}}
				>
					{#snippet badges()}<StatePill label={enumLabel(FederationRoleSchema, fed?.role ?? FederationRole.UNSPECIFIED)} lamp="primary" />{/snippet}
					{#snippet action()}<Confirm label="Drop" question="Drop {fed?.meshName || n.name}" danger onconfirm={() => drop(n)} />{/snippet}
				</ListRow>
			{:else}
				<Empty text="No federations" />
			{/each}
		</Stack>
	</Stack>
{/snippet}

{#snippet tuningList()}
	<Stack gap={24}>
		<Section title="Device profiles" count={tablesLoaded ? profiles.length : undefined} gap={0}>
			{#snippet tools()}
				<NeoButton elevation={2} onclick={() => editProfile()}>{#snippet icon()}<PlusIcon size={20} />{/snippet}Device profile</NeoButton>
			{/snippet}
			{#if !tablesLoaded}
				<Skeleton rows={3} />
			{:else}
				{#each profiles as p (p.pattern)}
					<ListRow
						primary={p.pattern}
						primaryTip={p.updatedAt ? `updated ${when(p.updatedAt)}` : undefined}
						secondary={streamRate(p.streamBytesPerSecond)}
						secondaryWidth="12ch"
						facts={[{ value: flopsRate(p.computeFlops), tip: 'compute' }, { value: seconds(p.fixedSeconds), tip: 'fixed per forward pass' }]}
						selected={editorOpen && editingPattern === p.pattern}
						onclick={() => editProfile(p)}
						menu={[{ label: 'Edit', onclick: () => editProfile(p) }, ...(p.builtin ? [] : [{ label: 'Delete', danger: true, divider: true, confirm: `Delete ${p.pattern}`, onclick: () => deleteProfile(p) }])]}
					>
						{#snippet badges()}<StatePill label={p.builtin ? 'builtin' : 'custom'} lamp={p.builtin ? 'muted' : 'primary'} />{/snippet}
					</ListRow>
				{:else}
					<Empty text="No device profiles" />
				{/each}
			{/if}
		</Section>
		<Section title="Ratios" count={tablesLoaded ? ratios.length : undefined} gap={0}>
			{#if !tablesLoaded}
				<Skeleton rows={2} />
			{:else}
				{#each ratios as r, i (i)}
					<ListRow
						primary={enumLabel(ShapeSchema, r.shape)}
						primaryTip={r.updatedAt ? `updated ${when(r.updatedAt)}` : undefined}
						secondary={r.runtimeId}
						secondaryWidth="16ch"
						facts={[{ value: `TTFT ${r.ttftRatio.toFixed(2)} · TPT ${r.tptRatio.toFixed(2)}` }, { value: `${num(r.samples)} samples` }]}
					>
						{#snippet badges()}<StatePill label={linkLabel(r.linkClass)} lamp={linkLamp(r.linkClass)} />{/snippet}
					</ListRow>
				{:else}
					<Empty text="Nothing learned" />
				{/each}
			{/if}
		</Section>
	</Stack>
{/snippet}

{#if !live.ready && !discovered}
	<Skeleton rows={4} />
{:else if !mesh}
	<Outside {status} oncreate={openCreate} onjoin={() => (joinOpen = true)} />
{:else}
	<Stack gap={16}>
		<MeshHeader {mesh} {self} />
		<PageTabs {tabs} {active} base="/mesh" />
		{#if active === 'nodes'}
			<Sheet open={id !== ''} onclose={close} list={nodesList} empty="Select a node">
				{#if selectedNode}
					{#key selectedNode.id}<NodePanel node={selectedNode} onclose={close} />{/key}
				{:else if !live.ready}
					<Skeleton rows={6} height={32} />
				{:else}
					<Empty text="No such node" action="Back" href="/mesh/nodes" />
				{/if}
			</Sheet>
		{:else if active === 'links'}
			<Sheet bind:open={linkOpen} list={linksList} empty="Select a node">
				{#if linkNode}
					{#key linkNode.id}<NodePanel node={linkNode} tab="links" onclose={() => (linkOpen = false)} />{/key}
				{/if}
			</Sheet>
		{:else if active === 'formations'}
			<Sheet open={id !== ''} onclose={closeFormation} list={formationsList} empty="Select a formation" width={formationLogs ? 720 : 480}>
				{#if selectedFormation}
					{#key selectedFormation.id}
						{#if formationLogs}
							<PanelHeader title={selectedFormation.name} subtitle="logs" onclose={closeFormation} />
							<LogView source={formationLogSource} name={selectedFormation.name} levels height="70vh" />
						{:else}
							<FormationPanel formation={selectedFormation} onclose={closeFormation} />
						{/if}
					{/key}
				{:else if formationError}
					<Empty text={formationError} action="Back" href="/mesh/formations" />
				{:else}
					<Skeleton rows={6} height={32} />
				{/if}
			</Sheet>
		{:else if active === 'nearby'}
			{@render nearbyView()}
		{:else if active === 'federation'}
			<Sheet bind:open={fedOpen} list={federationList} empty="Select a federation">
				{#if fedNode}
					{@const fed = fedNode.federation}
					{@const pools = fedPools(fedNode)}
					<PanelHeader title={fed?.meshName || fedNode.name} subtitle={enumLabel(FederationRoleSchema, fed?.role ?? FederationRole.UNSPECIFIED)} onclose={() => (fedOpen = false)} />
					<Stack gap={24}>
						<KeyRows
							rows={[
								{ label: 'node', value: fedNode.name },
								{ label: 'address', value: fed?.address || fedNode.address, mono: true },
								{ label: 'since', value: fed?.since ? ago(fed.since, clockTick.now) : '', tip: when(fed?.since) || undefined },
								{ label: 'fingerprint', value: fed?.fingerprint ?? '', mono: true, copy: true, tip: fed?.fingerprint },
								{ label: 'mesh hash', value: fed?.meshHash ?? '', mono: true, copy: true, tip: fed?.meshHash }
							]}
						/>
						<Section title="Pools" count={pools.length} gap={0}>
							{#each pools as { pool, href } (pool.id)}
								<ListRow lamp={poolLamp(pool)} shape={poolShape(pool)} primary={pool.id} secondary={pool.spec?.model ?? ''} secondaryKind="repo" facts={[{ value: poolWord(pool) }]} {href}>
									{#snippet action()}<NeoButton text {href}>Open</NeoButton>{/snippet}
								</ListRow>
							{:else}
								<Empty text="No pools" />
							{/each}
						</Section>
					</Stack>
				{/if}
			</Sheet>
		{:else}
			<Sheet bind:open={editorOpen} list={tuningList} empty="Select a profile">
				<PanelHeader title={editingPattern ?? 'New device profile'} onclose={() => (editorOpen = false)} />
				<Stack gap={16}>
					<FormSection title="Device profile" info="Declared numbers for devices whose name matches the pattern, used before any are learned">
						<FieldRow>
							<Text bind:value={pattern} label="Pattern" max={64} required readonly={editingPattern !== undefined} />
							<NumberField bind:value={fixedSeconds} min={0} step={0.001} label="Fixed" unit="s" info="Cost of one forward pass beyond streaming and compute" />
						</FieldRow>
						<FieldRow>
							<NumberField bind:value={streamGb} min={0} step={10} label="Stream" unit="GB/s" info="Weight streaming rate during decode" />
							<NumberField bind:value={tflops} min={0} step={10} label="Compute" unit="TFLOPS" info="Floating point rate during prefill" />
						</FieldRow>
					</FormSection>
					<SaveBar dirty={profileDirty} saving={profileSaving} error={profileError} message={pattern.trim() ? undefined : 'Pattern needed'} disabled={!pattern.trim()} label={editingPattern === undefined ? 'Create' : 'Save'} onsave={saveProfile} ondiscard={() => (editorOpen = false)} />
				</Stack>
			</Sheet>
		{/if}
	</Stack>
{/if}

<NeoDialog bind:open={createOpen} modal rounded="md" filled elevation={2} width="min(28rem, 92vw)" padding="16px">
	<Stack gap={16}>
		<h3>Create a mesh</h3>
		{#if made}
			<Screen><span class="token">{made.token}</span></Screen>
			<Row gap={12}>
				<Cell grow={false}><CopyButton text={made.token} label="Copy join token" /></Cell>
				<Cell min="8rem"><span class="secondary">shown once, expires <Time value={made.expires} /></span></Cell>
				<Cell grow={false} push><NeoButton class="primary" elevation={2} onclick={() => (createOpen = false)}>Done</NeoButton></Cell>
			</Row>
		{:else}
			<Text bind:value={meshName} label="Name" max={64} required />
			<Row gap={12} justify="flex-end">
				<NeoButton text onclick={() => (createOpen = false)}>Cancel</NeoButton>
				<NeoButton class="primary" elevation={2} loading={busy} disabled={!meshName.trim()} onclick={createMesh}>Create</NeoButton>
			</Row>
		{/if}
	</Stack>
</NeoDialog>

<NeoDialog bind:open={joinOpen} modal rounded="md" filled elevation={2} width="min(28rem, 92vw)" padding="16px">
	<Stack gap={16}>
		<h3>Join with a token</h3>
		<Text bind:value={joinToken} label="Token" mono required />
		<Row gap={12} justify="flex-end">
			<NeoButton text onclick={() => (joinOpen = false)}>Cancel</NeoButton>
			<NeoButton class="primary" elevation={2} loading={busy} disabled={!joinToken.trim()} onclick={join}>Join</NeoButton>
		</Row>
	</Stack>
</NeoDialog>

{#if forgetting}
	<NameConfirm bind:open={forgetOpen} title="Forget node" name={forgetting.name} message="Its certificate is revoked on every member" confirm="Forget" onconfirm={forget} />
{/if}

<style>
	.token {
		display: block;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
</style>
