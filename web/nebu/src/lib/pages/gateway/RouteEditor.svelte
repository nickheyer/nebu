<script lang="ts">
	import { clone, create, toJsonString } from '@bufbuild/protobuf';
	import { onMount, untrack } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import type { NeoSortableItem as SortItem } from '#lib/neo/sortable/neo-sortable-context.svelte.js';
	import { NeoSortableItem, NeoSortableProvider } from '#lib/neo/sortable/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { live } from '#lib/live/events.svelte.js';
	import { PolicySchema, ProfileSchema, RewriteRuleSchema, RewriteTargetSchema, RouteBalance, RouteSpecSchema, type RewriteRule, type Route, type RouteSpec } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { ToolServerState, type ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import MultiSelect from '#lib/ui/fields/MultiSelect.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import type { Option } from '#lib/ui/fields/types.js';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { isUserOwned, ownerLabel, targetShare } from '#lib/pages/gateway/gateway.js';
	import DotsSixVerticalIcon from 'phosphor-svelte/lib/DotsSixVerticalIcon';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import XIcon from 'phosphor-svelte/lib/XIcon';

	let { route, onclose }: { route?: Route; onclose: () => void } = $props();

	const start = untrack(() => route);
	const editing = start !== undefined;
	// A slot, instance or formation owns the rules, a node owns the whole route
	const owned = start !== undefined && !isUserOwned(start.ownerKind);
	const locked = start?.ownerKind === 'node';
	const rulesLocked = owned;
	const initial = start?.spec ? clone(RouteSpecSchema, start.spec) : create(RouteSpecSchema, { rules: [newRule()] });
	initial.policy ??= create(PolicySchema);
	initial.profile ??= create(ProfileSchema);
	const baseline = toJsonString(RouteSpecSchema, initial);

	let seq = 0;
	const firstKeys = initial.rules.map(() => nextKey());
	let name = $state(start?.name ?? '');
	let spec = $state(initial);
	// Rules by a key of their own, in the order the sortable list holds
	let rules = $state<Record<string, RewriteRule>>(Object.fromEntries(firstKeys.map((k, i) => [k, initial.rules[i]!])));
	let order = $state<SortItem<{ id: string }>[]>(firstKeys.map((k) => ({ id: k, data: { id: k } })));
	let servers = $state<ToolServerStatus[]>([]);
	let saving = $state(false);
	let error = $state<string | undefined>(undefined);

	const ordered = $derived(order.map((i) => rules[String(i.id)]).filter((r): r is RewriteRule => r !== undefined));
	const draft = $derived<RouteSpec>(create(RouteSpecSchema, { policy: spec.policy, profile: spec.profile, objective: spec.objective, balance: spec.balance, rules: ordered }));
	const dirty = $derived(!editing || toJsonString(RouteSpecSchema, draft) !== baseline);
	const current = $derived(editing ? live.routes.get(name) : undefined);
	const objectives = $derived([...live.objectives.values()].map((o) => ({ value: o.name, label: o.name, description: `priority ${o.priority}` })));
	const problem = $derived.by(() => {
		if (!name.trim()) return 'Name needed';
		if (ordered.length === 0) return 'One rule at least';
		if (ordered.some((r) => r.targets.length === 0)) return 'Every rule needs a target';
		if (ordered.some((r) => r.targets.some((t) => !t.poolId))) return 'Every target needs a pool';
		if (ordered.some((r) => r.targets.some((t) => t.weight < 1))) return 'Weights from 1';
		return undefined;
	});

	// Tool servers by the name routes attach them by, the live state of local ones winning
	const toolOptions = $derived.by((): Option<string>[] => {
		const byRef = new Map<string, ToolServerStatus>();
		for (const s of servers) byRef.set(s.ref || s.server?.name || '', s);
		for (const s of live.toolServers.values()) byRef.set(s.ref || s.server?.name || '', s);
		const options = [...byRef.entries()]
			.filter(([ref]) => ref !== '')
			.sort(([a], [b]) => a.localeCompare(b))
			.map(([ref, s]) => ({
				value: ref,
				label: ref,
				description: s.nodeId ? `on ${live.nodes.get(s.nodeId)?.name ?? s.nodeId}` : undefined,
				disabled: s.state === ToolServerState.FAILED && !spec.profile?.toolServers.includes(ref),
				tip: s.state === ToolServerState.FAILED ? s.error : undefined
			}));
		for (const ref of spec.profile?.toolServers ?? []) if (!byRef.has(ref)) options.push({ value: ref, label: ref, description: 'missing', disabled: false, tip: undefined });
		return options;
	});

	// Reads every tool server this node and the mesh members run, page by page
	onMount(async () => {
		try {
			const all: ToolServerStatus[] = [];
			let cursor = '';
			do {
				const res = await rpc.tools.listToolServers({ mesh: live.inMesh, cursor, limit: 200 });
				all.push(...res.servers);
				cursor = res.nextCursor;
			} while (cursor);
			servers = all;
		} catch (err) {
			toast.error('Tool servers failed to list', reason(err));
		}
	});

	function nextKey(): string {
		return `rule-${seq++}`;
	}

	function newRule(): RewriteRule {
		return create(RewriteRuleSchema, { targets: [create(RewriteTargetSchema, { weight: 1 })] });
	}

	function addRule() {
		const k = nextKey();
		rules[k] = newRule();
		order = [...order, { id: k, data: { id: k } }];
	}

	function removeRule(k: string) {
		order = order.filter((i) => i.id !== k);
		delete rules[k];
	}

	async function fetchPools(query: string, cursor: string, limit: number, signal: AbortSignal) {
		const res = await rpc.gateway.listPools({ query, cursor, limit }, { signal });
		return { items: res.pools.map((p) => ({ value: p.id, label: p.id, description: p.spec?.model })), next: res.nextCursor };
	}

	async function save() {
		saving = true;
		error = undefined;
		try {
			await rpc.gateway.putRoute({ name: name.trim(), spec: draft });
			toast.success(`Saved ${name.trim()}`);
			onclose();
		} catch (err) {
			error = reason(err);
		} finally {
			saving = false;
		}
	}

	async function remove() {
		try {
			await rpc.gateway.deleteRoute({ name });
			toast.success(`Deleted ${name}`);
			onclose();
		} catch (err) {
			toast.error('Delete route failed', reason(err));
		}
	}
</script>

{#snippet ruleBody(k: string, rule: RewriteRule, index: number, handle?: (node: HTMLElement) => () => void)}
	<Stack gap={12}>
		<Row gap={8} align="flex-end" wrap={false}>
			{#if handle}
				<Cell grow={false}>
					<span class="grip muted" role="button" tabindex="0" aria-label="Drag to reorder" {@attach handle}><DotsSixVerticalIcon size={20} /></span>
				</Cell>
			{/if}
			<Cell min="10rem">
				<Text bind:value={rule.match} label="Match {index + 1}" placeholder="every request" max={64} mono disabled={rulesLocked || locked} />
			</Cell>
			{#if !rulesLocked}
				<Cell grow={false}>
					<NeoButton text aria-label="Remove rule" onclick={() => removeRule(k)}>{#snippet icon()}<XIcon size={20} />{/snippet}</NeoButton>
				</Cell>
			{/if}
		</Row>
		{#each rule.targets as target, t (t)}
			<div class="target">
				<Stack gap={8}>
					<FieldRow>
						<Picker bind:value={target.poolId} fetch={fetchPools} label="Pool" current={target.poolId || undefined} required disabled={rulesLocked || locked} />
						<Text bind:value={target.model} label="Model" max={64} placeholder="pool model" disabled={rulesLocked || locked} />
					</FieldRow>
					<Row gap={8} align="flex-end" wrap={false}>
						<Cell min="10rem">
							<NumberField bind:value={target.weight} min={1} max={1_000_000} label="Weight" unit={targetShare(rule, target.weight)} disabled={rulesLocked || locked} />
						</Cell>
						{#if !rulesLocked}
							<Cell grow={false}>
								<NeoButton text aria-label="Remove target" disabled={rule.targets.length === 1} onclick={() => (rule.targets = rule.targets.filter((_, j) => j !== t))}>{#snippet icon()}<XIcon size={20} />{/snippet}</NeoButton>
							</Cell>
						{/if}
					</Row>
				</Stack>
			</div>
		{/each}
		{#if !rulesLocked}
			<Row gap={8}>
				<Cell grow={false}>
					<NeoButton text onclick={() => (rule.targets = [...rule.targets, create(RewriteTargetSchema, { weight: 1 })])}>{#snippet icon()}<PlusIcon size={20} />{/snippet}Target</NeoButton>
				</Cell>
			</Row>
		{/if}
	</Stack>
{/snippet}

<PanelHeader title={editing ? name : 'New route'} subtitle={owned && start ? ownerLabel(start.ownerKind, start.ownerId) : undefined} {onclose}>
	{#snippet tools()}
		{#if editing && !owned}<Confirm label="Delete" question="Delete {name}" danger onconfirm={remove} />{/if}
	{/snippet}
</PanelHeader>
<Stack gap={24}>
	<FormSection title="Route">
		<Text bind:value={name} label="Name" max={64} required readonly={editing} />
		<Select value={spec.objective || undefined} options={objectives} label="Objective" clearable placeholder="None" disabled={locked} onchange={(v) => (spec.objective = v ?? '')} />
		<Segmented
			bind:value={spec.balance}
			options={[
				{ value: RouteBalance.UNSPECIFIED, label: 'Weights' },
				{ value: RouteBalance.READY_ENDPOINTS, label: 'Ready endpoints' },
				{ value: RouteBalance.FREE_CAPACITY, label: 'Free capacity' }
			]}
			label="Balance"
			info="How every rule weighs its targets beyond their static weights"
			disabled={locked}
		/>
	</FormSection>
	<FormSection title="Rules" info={rulesLocked ? `Set by ${start ? ownerLabel(start.ownerKind, start.ownerId) : 'the owner'}` : 'In order, the first exact match winning over a catch all'}>
		{#if rulesLocked}
			{#each order as item, index (item.id)}
				{@const rule = rules[String(item.id)]}
				{#if rule}<div class="rule hairline">{@render ruleBody(String(item.id), rule, index)}</div>{/if}
			{/each}
		{:else}
			<NeoSortableProvider bind:items={order} axis="y">
				{#each order as item, index (item.id)}
					{@const k = String(item.id)}
					{@const rule = rules[k]}
					{#if rule}
						<NeoSortableItem id={item.id} {index} data={item.data}>
							{#snippet children({ instance })}
								<div class="rule hairline" {@attach instance.attach}>{@render ruleBody(k, rule, index, instance.attachHandle)}</div>
							{/snippet}
						</NeoSortableItem>
					{/if}
				{/each}
			</NeoSortableProvider>
			<Row gap={8}>
				<Cell grow={false}><NeoButton elevation={2} onclick={addRule}>{#snippet icon()}<PlusIcon size={20} />{/snippet}Rule</NeoButton></Cell>
			</Row>
		{/if}
	</FormSection>
	{#if spec.policy}
		<FormSection title="Policy" info="Zero inherits the gateway default">
			<FieldRow>
				<NumberField bind:value={spec.policy.maxInFlight} min={0} max={1_000_000} label="Max in flight" emptyZero placeholder="inherits" disabled={locked} />
				<NumberField bind:value={spec.policy.requestsPerSecond} min={0} step={0.5} label="Requests per second" emptyZero placeholder="inherits" disabled={locked} />
			</FieldRow>
			<FieldRow>
				<Duration bind:value={spec.policy.requestTimeoutMs} base="ms" max={86_400_000} label="Request timeout" info="First byte in to last byte out" disabled={locked} />
				<Duration bind:value={spec.policy.upstreamTimeoutMs} base="ms" max={86_400_000} label="Upstream timeout" info="Wait for the runtime to start answering" disabled={locked} />
			</FieldRow>
			{#snippet advanced()}
				{#if spec.policy}
					<FieldRow>
						<NumberField bind:value={spec.policy.burst} min={0} max={1_000_000} label="Burst" info="Requests a quiet route absorbs at once, zero rounds the rate up" emptyZero placeholder="inherits" disabled={locked} />
					</FieldRow>
				{/if}
			{/snippet}
		</FormSection>
	{/if}
	{#if spec.profile}
		<FormSection title="Tools">
			{#if toolOptions.length > 8}
				<MultiSelect bind:value={spec.profile.toolServers} options={toolOptions} label="Tool servers" search disabled={locked} />
			{:else}
				<Checklist bind:value={spec.profile.toolServers} options={toolOptions} label="Tool servers" empty="No tool servers" disabled={locked} />
			{/if}
			<Switch bind:checked={spec.profile.mutatingTools} label="Mutating tools" info="Tools that change what they touch, read only tools alone when off" disabled={locked} />
		</FormSection>
	{/if}
	{#if current}
		<Section title="Conditions">
			<Conditions conditions={current.status?.conditions} />
		</Section>
	{/if}
</Stack>
{#if !locked}
	<SaveBar {dirty} {saving} {error} message={problem} label={editing ? 'Save' : 'Create'} disabled={problem !== undefined} onsave={save} ondiscard={onclose} />
{/if}

<style>
	.rule {
		padding: 12px 0;
	}
	.target {
		padding-left: 32px;
	}
	.grip {
		display: inline-flex;
		padding-bottom: 8px;
		cursor: grab;
		touch-action: none;
	}
</style>
