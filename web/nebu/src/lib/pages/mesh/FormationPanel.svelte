<script lang="ts">
	import { toJsonString } from '@bufbuild/protobuf';
	import Collapse from '#lib/ui/Collapse.svelte';
	import { bytes, enumLabel, ms, num, plural, tokensPerSecond } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { FitVerdictSchema, ShapeSchema } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { RunRequestSchema } from '#lib/proto/nebu/v1/instance_pb.js';
	import { FormationStateSchema, type Formation, type FormationPlan } from '#lib/proto/nebu/v1/mesh_pb.js';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import MemoryBars from '#lib/ui/MemoryBars.svelte';
	import MemoryLegend from '#lib/ui/MemoryLegend.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Str from '#lib/ui/Str.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { formationLamp, instanceLamp, instanceShape } from '#lib/ui/status.js';
	import { candidateTip, linkLabel, peerName, seatKey, seatLayers, seatName, seatTip, speedup, verdictLamp } from '#lib/pages/mesh/mesh.js';

	let { formation, onclose }: { formation: Formation; onclose: () => void } = $props();

	let tab = $state('seats');
	const f = $derived(formation);
	const tabs = $derived([
		{ id: 'seats', label: 'Seats', count: f.seats.length },
		{ id: 'plan', label: 'Plan', count: f.plan?.candidates.length },
		{ id: 'memory', label: 'Memory' },
		{ id: 'conditions', label: 'Conditions', count: f.conditions.length }
	]);

	function planNodes(p: FormationPlan): string {
		return p.seats.map((s) => seatName(live, s)).join(', ');
	}

	// Prefill, decode and the planner's note for the chosen plan, one per line
	function planTip(p: FormationPlan): string {
		return [`prefill ${ms(p.prefillSeconds * 1000)}`, `decode ${ms(p.decodeSecondsPerToken * 1000)} per token`, `context ${num(p.context)}`, p.detail].filter(Boolean).join('\n');
	}
</script>

<PanelHeader title={f.name} subtitle="{enumLabel(ShapeSchema, f.shape)} · {plural(f.seats.length, 'node')}" {onclose}>
	<StatePill label={enumLabel(FormationStateSchema, f.state)} lamp={formationLamp(f.state)} />
</PanelHeader>
<Stack gap={16}>
	<PageTabs {tabs} active={tab} onchange={(x) => (tab = x)} />
	{#if tab === 'seats'}
		{#if f.seats.length}
			<List head={{ primary: 'Node', secondary: 'Role', facts: ['Layers'] }}>
				{#each f.seats as s (seatKey(s))}
					<ListRow lamp={instanceLamp(s.state)} shape={instanceShape(s.state)} primary={seatName(live, s)} primaryTip={seatTip(s)} secondary="{s.role || 'seat'} {s.rank}" secondaryWidth="16ch" facts={[{ value: seatLayers(s) }]}>
						{#snippet expand()}
							{#if s.error}<p class="error-text">{s.error}</p>{/if}
							{#each s.triage as t (t.id)}
								<Str value={t.hint ? `${t.summary}, ${t.hint}` : t.summary} kind="message" tip={[t.summary, t.hint, t.line].filter(Boolean).join('\n')} width="100%" />
							{/each}
							{#if s.measurements.length}
								<KeyRows dense rows={s.measurements.map((m) => ({ label: m.key, value: bytes(m.bytes), tip: m.line || undefined }))} />
							{:else if !s.error && !s.triage.length}
								<Empty text="Nothing measured" row />
							{/if}
						{/snippet}
					</ListRow>
				{/each}
			</List>
		{:else}
			<Empty text="No seats" hint="The conductor places a seat on each member the plan names" />
		{/if}
	{:else if tab === 'plan'}
		{#if f.plan}
			{@const plan = f.plan}
			<Stack gap={24}>
				<Section title="Chosen" level={3}>
					<List head={{ primary: 'Shape', secondary: 'Nodes', facts: ['Speed', 'Gain'] }}>
						<ListRow
							lamp={verdictLamp(plan.verdict)}
							primary={enumLabel(ShapeSchema, plan.shape)}
							primaryTip={planTip(plan)}
							secondary={planNodes(plan)}
							secondaryWidth="24ch"
							facts={[{ value: tokensPerSecond(plan.tokensPerSecond) }, { value: speedup(plan.speedup) }]}
						>
							{#snippet badges()}<StatePill label={enumLabel(FitVerdictSchema, plan.verdict)} lamp={verdictLamp(plan.verdict)} />{/snippet}
						</ListRow>
					</List>
				</Section>
				<Section title="Candidates" count={plan.candidates.length} level={3}>
					{#if plan.candidates.length}
						<List head={{ primary: 'Shape', secondary: 'Nodes', facts: ['Speed', 'Gain'] }}>
							{#each plan.candidates as c, i (i)}
								<ListRow
									lamp={verdictLamp(c.verdict)}
									primary={enumLabel(ShapeSchema, c.shape)}
									primaryTip={candidateTip(c)}
									secondary={c.nodeIds.map((id) => peerName(live, id)).join(', ')}
									secondaryWidth="24ch"
									facts={[{ value: tokensPerSecond(c.tokensPerSecond) }, { value: speedup(c.speedup) }]}
								>
									{#snippet badges()}<StatePill label={enumLabel(FitVerdictSchema, c.verdict)} lamp={verdictLamp(c.verdict)} />{/snippet}
								</ListRow>
							{/each}
						</List>
					{:else}
						<Empty text="No other candidates" row />
					{/if}
				</Section>
				<Collapse label="Ratios and sources">
					<KeyRows
						dense
						rows={[
							{ label: 'link class', value: linkLabel(plan.linkClass) },
							{ label: 'TTFT ratio', value: plan.ttftRatio ? plan.ttftRatio.toFixed(2) : 'none' },
							{ label: 'TPT ratio', value: plan.tptRatio ? plan.tptRatio.toFixed(2) : 'none' },
							{ label: 'samples', value: num(plan.ratioSamples) },
							...plan.sources.map((s, i) => ({ label: `source ${i + 1}`, value: s, tip: s }))
						]}
					/>
				</Collapse>
			</Stack>
		{:else}
			<Empty text="No plan" hint="The plan lands once the conductor has priced the candidates" />
		{/if}
	{:else if tab === 'memory'}
		{#if f.seats.length}
			<Stack gap={24}>
				{#each f.seats as s (seatKey(s))}
					{@const seatNode = live.nodes.get(s.nodeId)}
					<Section title="{seatName(live, s)} {s.role || 'seat'} {s.rank}" level={3}>
						<MemoryBars profile={seatNode?.profile} instances={seatNode?.self ? live.instances.values() : []} slots={seatNode?.self ? live.slots.values() : []} plan={s.memory} only={s.deviceIds.length ? s.deviceIds : undefined} legend={false} />
					</Section>
				{/each}
				<MemoryLegend kinds={['instance', 'planned', 'other', 'free']} />
			</Stack>
		{:else}
			<Empty text="No seats" hint="Each seat's memory plan shows here once placed" />
		{/if}
	{:else if f.conditions.length || f.error}
		<Stack gap={8}>
			{#if f.conditions.length}<Conditions conditions={f.conditions} />{/if}
			{#if f.error}<p class="error-text">{f.error}</p>{/if}
		</Stack>
	{:else}
		<Empty text="No conditions reported" hint="The conductor reports placement, launch and serving checks here" />
	{/if}
	<Collapse label="Request">
		{#if f.request}
			<Screen maxHeight="50vh"><pre class="lines">{toJsonString(RunRequestSchema, f.request, { prettySpaces: 2 })}</pre></Screen>
		{:else}
			<Empty text="No request recorded" row />
		{/if}
	</Collapse>
</Stack>

<style>
	.lines {
		margin: 0;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
</style>
