<script lang="ts">
	import { clone, create, toJsonString } from '@bufbuild/protobuf';
	import { untrack } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import type { NeoSortableItem as SortItem } from '#lib/neo/sortable/neo-sortable-context.svelte.js';
	import { NeoSortableItem, NeoSortableProvider } from '#lib/neo/sortable/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { live } from '#lib/live/events.svelte.js';
	import { PolicySchema, ProfileSchema, RewriteRuleSchema, RewriteTargetSchema, RouteBalance, RouteSpecSchema, type RewriteRule, type Route, type RouteSpec } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Dot from '#lib/ui/Dot.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { isUserOwned, ownerLabel, ownerLink, poolLamp, poolShape, poolWord, targetShare } from '#lib/pages/gateway/gateway.js';
	import PolicyFields from '#lib/pages/serve/PolicyFields.svelte';
	import DotsSixVerticalIcon from 'phosphor-svelte/lib/DotsSixVerticalIcon';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import XIcon from 'phosphor-svelte/lib/XIcon';

	let { route, onclose }: { route?: Route; onclose: () => void } = $props();

	const start = untrack(() => route);
	const editing = start !== undefined;
	const owned = start !== undefined && !isUserOwned(start.ownerKind);
	const locked = start?.ownerKind === 'node';
	const owner = start ? ownerLink(live, start) : undefined;
	const initial = start?.spec ? clone(RouteSpecSchema, start.spec) : create(RouteSpecSchema, { rules: [newRule()] });
	initial.policy ??= create(PolicySchema);
	initial.profile ??= create(ProfileSchema);
	const baseline = toJsonString(RouteSpecSchema, initial);

	let seq = 0;
	const firstKeys = initial.rules.map(() => nextKey());
	let name = $state(start?.name ?? '');
	let spec = $state(initial);
	let rules = $state<Record<string, RewriteRule>>(Object.fromEntries(firstKeys.map((k, i) => [k, initial.rules[i]!])));
	let order = $state<SortItem<{ id: string }>[]>(firstKeys.map((k) => ({ id: k, data: { id: k } })));
	let saving = $state(false);
	let error = $state<string | undefined>(undefined);

	const ordered = $derived(order.map((i) => rules[String(i.id)]).filter((r): r is RewriteRule => r !== undefined));
	const draft = $derived<RouteSpec>(create(RouteSpecSchema, { policy: spec.policy, profile: spec.profile, objective: spec.objective, balance: spec.balance, rules: ordered }));
	const dirty = $derived(!editing || toJsonString(RouteSpecSchema, draft) !== baseline);
	const current = $derived(editing ? live.routes.get(name) : undefined);
	const objectives = $derived([...live.objectives.values()].map((o) => ({ value: o.name, label: o.name, description: `priority ${o.priority}` })));
	const complete = $derived(!locked && name.trim() !== '' && ordered.length > 0 && ordered.every((r) => r.targets.length > 0 && r.targets.every((t) => t.poolId && t.weight >= 1)));

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

{#snippet removeButton(label: string, onclick: () => void, disabled = false)}
	<span class="trail remove"><NeoButton text aria-label={label} title={label} {disabled} {onclick}>{#snippet icon()}<XIcon size={18} />{/snippet}</NeoButton></span>
{/snippet}

{#snippet ruleBody(k: string, rule: RewriteRule, index: number, handle?: (node: HTMLElement) => () => void)}
	<div class="rulehead">
		{#if handle}
			<span class="grip muted" role="button" tabindex="0" aria-label="Drag to reorder" title="Drag to reorder" {@attach handle}><DotsSixVerticalIcon size={18} /></span>
		{/if}
		<span class="label">Rule {index + 1}</span>
		<span class="fill"></span>
		{#if !owned}{@render removeButton('Remove rule', () => removeRule(k), ordered.length === 1)}{/if}
	</div>
	<div class="rulebody">
		<Text bind:value={rule.match} label="Match" info="Exact model name, every request when empty" max={64} mono disabled={owned || locked} />
		<div class="targets">
			{#each rule.targets as target, t (t)}
				{@const pool = target.poolId ? live.pools.get(target.poolId) : undefined}
				<div class="target">
					<span class="pool"><Picker bind:value={target.poolId} fetch={fetchPools} label={t === 0 ? 'Pool' : undefined} current={target.poolId || undefined} required disabled={owned || locked} /></span>
					<span class="model"><Text bind:value={target.model} label={t === 0 ? 'Model' : undefined} info={t === 0 ? 'Sent to the pool in place of the name asked for' : undefined} max={64} placeholder={pool?.spec?.model || undefined} disabled={owned || locked} /></span>
					<span class="weight"><NumberField bind:value={target.weight} min={1} max={1_000_000} label={t === 0 ? 'Weight' : undefined} unit={targetShare(rule, target.weight)} disabled={owned || locked} /></span>
					{#if target.poolId}
						<span class="trail share"><Tip text={poolWord(pool)} tag="span"><Dot lamp={poolLamp(pool)} shape={poolShape(pool)} label={poolWord(pool)} /></Tip></span>
					{/if}
					{#if !owned}{@render removeButton('Remove target', () => (rule.targets = rule.targets.filter((_, j) => j !== t)), rule.targets.length === 1)}{/if}
				</div>
			{/each}
		</div>
		{#if !owned}
			<span class="add"><NeoButton text onclick={() => (rule.targets = [...rule.targets, create(RewriteTargetSchema, { weight: 1 })])}>{#snippet icon()}<PlusIcon size={18} />{/snippet}Target</NeoButton></span>
		{/if}
	</div>
{/snippet}

<PanelHeader title={editing ? name : 'New route'} subtitle={owned && start ? ownerLabel(start.ownerKind, start.ownerId) : undefined} {onclose}>
	{#snippet tools()}
		{#if owner}
			<NeoButton elevation={2} href={owner.href}>{owner.label}</NeoButton>
		{/if}
	{/snippet}
</PanelHeader>
<Stack gap={24}>
	<FormSection title="Route">
		<FieldRow>
			<Text bind:value={name} label="Name" info="Fixed once created" required readonly={editing} max={64} />
			<Select value={spec.objective || undefined} options={objectives} label="Objective" info="Requests may name their own" clearable disabled={locked} onchange={(v) => (spec.objective = v ?? '')} />
		</FieldRow>
		<Segmented
			bind:value={spec.balance}
			options={[
				{ value: RouteBalance.UNSPECIFIED, label: 'Weights' },
				{ value: RouteBalance.READY_ENDPOINTS, label: 'Ready endpoints' },
				{ value: RouteBalance.FREE_CAPACITY, label: 'Free capacity' }
			]}
			label="Balance" info="What target weights are scaled by"
			disabled={locked}
		/>
	</FormSection>
	<FormSection title="Rules" info="The first rule that matches wins">
		<div class="rules">
			{#if owned}
				{#each order as item, index (item.id)}
					{@const rule = rules[String(item.id)]}
					{#if rule}<div class="rule">{@render ruleBody(String(item.id), rule, index)}</div>{/if}
				{/each}
			{:else}
				<NeoSortableProvider bind:items={order} axis="y">
					{#each order as item, index (item.id)}
						{@const k = String(item.id)}
						{@const rule = rules[k]}
						{#if rule}
							<NeoSortableItem id={item.id} {index} data={item.data}>
								{#snippet children({ instance })}
									<div class="rule" {@attach instance.attach}>{@render ruleBody(k, rule, index, instance.attachHandle)}</div>
								{/snippet}
							</NeoSortableItem>
						{/if}
					{/each}
				</NeoSortableProvider>
			{/if}
		</div>
		{#if !owned}
			<span class="add"><NeoButton elevation={2} onclick={addRule}>{#snippet icon()}<PlusIcon size={18} />{/snippet}Rule</NeoButton></span>
		{/if}
	</FormSection>
	{#if spec.policy && spec.profile}
		<PolicyFields bind:policy={spec.policy} bind:profile={spec.profile} disabled={locked} />
	{/if}
	{#if current?.status?.conditions.length}
		<Section title="Status" level={3}>
			<Conditions conditions={current.status.conditions} />
		</Section>
	{/if}
</Stack>
{#snippet deleteButton()}
	<Confirm label="Delete" question="Delete {name}?" danger placement="top-start" onconfirm={remove} />
{/snippet}

<SaveBar dirty={locked ? false : dirty} always={!editing} {saving} {error} message={locked ? 'Read only' : undefined} label={editing ? 'Save' : 'Create'} disabled={!complete} leading={editing && !owner ? deleteButton : undefined} onsave={save} ondiscard={onclose} />

<style>
	.rules {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.rule {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-3);
		padding: var(--nebu-space-3) 0 var(--nebu-space-4);
		border-top: 1px solid var(--nebu-border);
	}
	.rule:last-child {
		border-bottom: 1px solid var(--nebu-border);
	}
	.rulehead {
		display: flex;
		gap: var(--nebu-space-2);
		align-items: center;
		min-height: 32px;
	}
	.grip {
		display: inline-flex;
		cursor: grab;
		touch-action: none;
	}
	.rulebody {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-3);
		min-width: 0;
		padding-left: 26px;
	}
	.targets {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
		container-type: inline-size;
	}
	.target {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-2) var(--nebu-space-3);
		align-items: flex-end;
		min-width: 0;
	}
	.pool {
		display: flex;
		flex: 3 1 11rem;
		min-width: 0;
	}
	.model {
		display: flex;
		flex: 2 1 9rem;
		min-width: 0;
	}
	.pool > :global(*),
	.model > :global(*) {
		flex: 1 1 auto;
		min-width: 0;
	}
	.weight {
		display: flex;
		flex: 0 0 11rem;
		min-width: 0;
	}
	.weight > :global(*) {
		flex: 1 1 auto;
	}
	.share {
		margin-left: auto;
	}
	.remove {
		flex: 0 0 auto;
	}
	@container (max-width: 39rem) {
		.pool,
		.model {
			flex: 1 1 calc(50% - var(--nebu-space-3));
		}
		.target + .target {
			margin-top: var(--nebu-space-3);
		}
	}
	.add {
		display: inline-flex;
		align-self: flex-start;
	}
</style>
