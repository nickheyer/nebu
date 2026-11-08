<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { reason, rpc } from '#lib/api/transport.js';
	import { enumLabel, num, seconds, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { ShapeSchema } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { DeviceProfileSchema, type DeviceProfile, type FormationRatio } from '#lib/proto/nebu/v1/mesh_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import { flopsRate, linkLabel, linkLamp, streamRate } from '#lib/pages/mesh/mesh.js';
	import GaugeIcon from 'phosphor-svelte/lib/GaugeIcon';

	// The planner's device profiles and the speed ratios learned from formations, profiles edited in the panel
	let profiles = $state<DeviceProfile[]>([]);
	let ratios = $state<FormationRatio[]>([]);
	let loaded = $state(false);

	async function load() {
		try {
			const res = await rpc.mesh.getMesh({});
			profiles = res.profiles;
			ratios = res.ratios;
		} catch (err) {
			toast.error('Tuning failed to load', reason(err));
		} finally {
			loaded = true;
		}
	}

	// Loads the tables, again a second after mesh changes settle
	$effect(() => {
		void load();
		let timer: ReturnType<typeof setTimeout> | undefined;
		const off = live.on(EventKind.MESH, () => {
			clearTimeout(timer);
			timer = setTimeout(() => void load(), 1000);
		});
		return () => {
			clearTimeout(timer);
			off();
		};
	});

	let editorOpen = $state(false);
	let editing = $state<string | undefined>(undefined);
	let pattern = $state('');
	let streamGb = $state(0);
	let tflops = $state(0);
	let fixedSeconds = $state(0);
	let start = $state({ pattern: '', streamGb: 0, tflops: 0, fixedSeconds: 0 });
	let saving = $state(false);
	let saveError = $state<string | undefined>(undefined);
	const dirty = $derived(editing === undefined || pattern !== start.pattern || streamGb !== start.streamGb || tflops !== start.tflops || fixedSeconds !== start.fixedSeconds);
	const missing = $derived([!pattern.trim() ? 'pattern' : '', streamGb <= 0 ? 'stream' : '', tflops <= 0 ? 'compute' : ''].filter(Boolean));

	// Opens the editor on a profile, or on a new one
	export function edit(p?: DeviceProfile) {
		editing = p?.pattern;
		pattern = p?.pattern ?? '';
		streamGb = p ? p.streamBytesPerSecond / 1e9 : 0;
		tflops = p ? p.computeFlops / 1e12 : 0;
		fixedSeconds = p ? p.fixedSeconds : 0;
		start = { pattern, streamGb, tflops, fixedSeconds };
		saveError = undefined;
		editorOpen = true;
	}

	async function save() {
		saving = true;
		saveError = undefined;
		try {
			const profile = create(DeviceProfileSchema, { pattern: pattern.trim(), streamBytesPerSecond: streamGb * 1e9, computeFlops: tflops * 1e12, fixedSeconds });
			profiles = (await rpc.mesh.setDeviceProfile({ profile })).profiles;
			toast.success(`Saved ${profile.pattern}`);
			editorOpen = false;
		} catch (err) {
			saveError = reason(err);
		} finally {
			saving = false;
		}
	}

	async function remove(p: DeviceProfile) {
		try {
			profiles = (await rpc.mesh.deleteDeviceProfile({ pattern: p.pattern })).profiles;
			toast.success(`Deleted ${p.pattern}`);
			if (editing === p.pattern) editorOpen = false;
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}
</script>

{#snippet list()}
	<Card title="Device profiles" count={loaded ? profiles.length : undefined} info="Declared numbers for devices whose name matches the pattern, used before any are learned" flush>
		{#if !loaded}
			<Skeleton rows={4} dot={false} secondary="12ch" facts={2} action={false} />
		{:else if profiles.length === 0}
			<Empty icon={GaugeIcon} text="No device profiles" hint="A profile declares a device's streaming and compute rates for the planner" action="New profile" onclick={() => edit()} />
		{:else}
			<List head={{ primary: 'Pattern', secondary: 'Stream', facts: ['Compute', 'Fixed'] }}>
				{#each profiles as p (p.pattern)}
					<ListRow
						primary={p.pattern}
						primaryTip={p.updatedAt ? `updated ${when(p.updatedAt)}` : undefined}
						secondary={streamRate(p.streamBytesPerSecond)}
						secondaryWidth="12ch"
						facts={[{ value: flopsRate(p.computeFlops), tip: 'compute' }, { value: seconds(p.fixedSeconds), tip: 'fixed per forward pass' }]}
						selected={editorOpen && editing === p.pattern}
						onclick={() => edit(p)}
						menu={p.builtin ? undefined : [{ label: 'Delete', danger: true, confirm: `Delete ${p.pattern}`, onclick: () => remove(p) }]}
					>
						{#snippet badges()}<StatePill label={p.builtin ? 'builtin' : 'custom'} lamp={p.builtin ? 'muted' : 'primary'} />{/snippet}
					</ListRow>
				{/each}
			</List>
		{/if}
	</Card>
	{#if loaded && ratios.length}
		<Card title="Ratios" count={ratios.length} info="Measured over planned speed of earlier formations, by shape, runtime and link class" flush>
			<List head={{ primary: 'Shape', secondary: 'Runtime', facts: ['TTFT and TPT', 'Samples'] }}>
				{#each ratios as r, i (i)}
					<ListRow
						primary={enumLabel(ShapeSchema, r.shape)}
						primaryTip={r.updatedAt ? `updated ${when(r.updatedAt)}` : undefined}
						secondary={r.runtimeId}
						secondaryWidth="16ch"
						facts={[{ value: `${r.ttftRatio.toFixed(2)} · ${r.tptRatio.toFixed(2)}`, tip: 'TTFT ratio · TPT ratio' }, { value: num(r.samples) }]}
					>
						{#snippet badges()}<StatePill label={linkLabel(r.linkClass)} lamp={linkLamp(r.linkClass)} />{/snippet}
					</ListRow>
				{/each}
			</List>
		</Card>
	{/if}
{/snippet}

<Sheet bind:open={editorOpen} {list}>
	<PanelHeader title={editing ?? 'New device profile'} onclose={() => (editorOpen = false)} />
	<FormSection title="Device profile" info="Declared numbers for devices whose name matches the pattern, used before any are learned">
		<FieldRow>
			<Text bind:value={pattern} label="Pattern" max={64} required readonly={editing !== undefined} />
			<NumberField bind:value={fixedSeconds} min={0} step={0.001} label="Fixed" unit="s" placeholder="None" emptyZero info="Cost of one forward pass beyond streaming and compute" />
		</FieldRow>
		<FieldRow>
			<NumberField bind:value={streamGb} min={0} step={10} label="Stream" unit="GB/s" placeholder="Required" emptyZero info="Weight streaming rate during decode" />
			<NumberField bind:value={tflops} min={0} step={10} label="Compute" unit="TFLOPS" placeholder="Required" emptyZero info="Floating point rate during prefill" />
		</FieldRow>
	</FormSection>
	<SaveBar always={editing === undefined} {dirty} {saving} error={saveError} message={missing.length ? `Needs ${missing.join(', ')}` : undefined} disabled={missing.length > 0} label={editing === undefined ? 'Create' : 'Save'} onsave={save} ondiscard={() => (editorOpen = false)} />
</Sheet>
