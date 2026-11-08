<script lang="ts">
	import { clone, create, toJsonString } from '@bufbuild/protobuf';
	import { untrack } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { live } from '#lib/live/events.svelte.js';
	import { RuntimeInventorySchema } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { AppProtocol, FailureMode, MetricNamesSchema, PoolSpecSchema, StaticEndpointSchema, type Pool } from '#lib/proto/nebu/v1/pool_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import KeyValues from '#lib/ui/fields/KeyValues.svelte';
	import MultiSelect from '#lib/ui/fields/MultiSelect.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import XIcon from 'phosphor-svelte/lib/XIcon';

	let { pool, onclose }: { pool?: Pool; onclose: () => void } = $props();

	const start = untrack(() => pool);
	const editing = start !== undefined;
	const initial = start?.spec ? clone(PoolSpecSchema, start.spec) : create(PoolSpecSchema, { targetPorts: [8000], appProtocol: AppProtocol.HTTP, failureMode: FailureMode.FAIL_CLOSE, export: true });
	initial.metrics ??= create(MetricNamesSchema);
	initial.inventory ??= create(RuntimeInventorySchema);
	initial.export ??= false;
	const baseline = toJsonString(PoolSpecSchema, initial);

	let id = $state(start?.id ?? '');
	let spec = $state(initial);
	let ports = $state(initial.targetPorts.map(String));
	let saving = $state(false);
	let error = $state<string | undefined>(undefined);

	const portNumbers = $derived(ports.map(Number));
	const dirty = $derived(!editing || toJsonString(PoolSpecSchema, spec) !== baseline || portNumbers.join(',') !== initial.targetPorts.join(','));
	const problem = $derived.by(() => {
		if (!id.trim()) return 'Id needed';
		if (!spec.model.trim()) return 'Model needed';
		if (portNumbers.length === 0) return 'One target port at least';
		if (portNumbers.some((n) => !Number.isInteger(n) || n < 1 || n > 65535)) return 'Ports from 1 to 65535';
		if (spec.staticEndpoints.some((e) => !e.address.trim())) return 'Static endpoint address needed';
		return undefined;
	});

	// Runtimes installed here and those other pools name, the current choice kept
	const runtimes = $derived(
		[...new Set([...[...live.installs.values()].map((i) => i.runtimeId), ...[...live.pools.values()].map((p) => p.spec?.runtimeId ?? ''), spec.runtimeId])]
			.filter(Boolean)
			.sort()
			.map((r) => ({ value: r, label: r }))
	);
	const configs = $derived([...live.schedulerConfigs.values()].map((c) => ({ value: c.name, label: c.name })));
	const modalities = ['text', 'image', 'audio', 'video', 'embedding', 'rerank'].map((m) => ({ value: m, label: m }));

	function addEndpoint() {
		spec.staticEndpoints = [...spec.staticEndpoints, create(StaticEndpointSchema)];
	}

	async function save() {
		saving = true;
		error = undefined;
		try {
			spec.targetPorts = portNumbers;
			await rpc.gateway.putPool({ id: id.trim(), spec });
			toast.success(`Saved ${id.trim()}`);
			onclose();
		} catch (err) {
			error = reason(err);
		} finally {
			saving = false;
		}
	}

	async function remove() {
		try {
			await rpc.gateway.deletePool({ id });
			toast.success(`Deleted ${id}`);
			onclose();
		} catch (err) {
			toast.error('Delete pool failed', reason(err));
		}
	}
</script>

<PanelHeader title={editing ? id : 'New pool'} {onclose}>
	{#snippet tools()}
		{#if editing}<Confirm label="Delete" question="Delete {id}?" danger onconfirm={remove} />{/if}
	{/snippet}
</PanelHeader>
<Stack gap={24}>
	<FormSection title="Pool">
		<FieldRow>
			<Text bind:value={id} label="Id" required readonly={editing} max={64} placeholder="llama-70b" />
			<Text bind:value={spec.model} label="Model" required max={128} placeholder="meta-llama/Llama-3.3-70B-Instruct" />
		</FieldRow>
		<FieldRow>
			<Select value={spec.runtimeId || undefined} options={runtimes} label="Runtime" clearable placeholder="Any" onchange={(v) => (spec.runtimeId = v ?? '')} />
			<Text bind:value={spec.accelerator} label="Accelerator" max={64} placeholder="2x NVIDIA H100 80GB HBM3" />
		</FieldRow>
		<KeyValues bind:value={spec.selector} label="Selector" info="Labels an endpoint carries to join" />
		<FieldRow>
			<Chips bind:value={ports} label="Target ports" numeric max={8} info="Up to eight, 1 to 65535" />
			<NumberField bind:value={spec.concurrency} min={0} label="Concurrency" emptyZero placeholder="Default" info="Requests one endpoint runs at once when the runtime reports no capacity" />
		</FieldRow>
		<FieldRow>
			<Segmented bind:value={spec.appProtocol} options={[{ value: AppProtocol.HTTP, label: 'HTTP' }, { value: AppProtocol.H2C, label: 'h2c' }, { value: AppProtocol.GRPC, label: 'gRPC' }]} label="Protocol" />
			<Segmented bind:value={spec.failureMode} options={[{ value: FailureMode.FAIL_CLOSE, label: 'Close' }, { value: FailureMode.FAIL_OPEN, label: 'Open' }]} label="Failure mode" info="Whether requests pass when the scheduler fails" />
		</FieldRow>
		<FieldRow>
			<Select value={spec.schedulerConfig || undefined} options={configs} label="Scheduler config" clearable placeholder="Built in" onchange={(v) => (spec.schedulerConfig = v ?? '')} />
			<Switch bind:checked={spec.export} label="Export" info="Offered to every mesh member and federated meshes" />
		</FieldRow>
		<FieldRow>
			<Switch bind:checked={spec.tokensInOut} label="Tokens in and out" info="The gateway encodes prompts and sends token ids" />
			<span></span>
		</FieldRow>
		{#snippet advanced()}
			{#if spec.inventory}
				<FieldRow>
					<NumberField bind:value={spec.inventory.contextLength} min={0} label="Context length" unit="tokens" emptyZero placeholder="Default" />
					<Text bind:value={spec.inventory.quantization} label="Quantization" max={32} placeholder="Q4_K_M" />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.inventory.accelerator} label="Launch accelerator" max={64} placeholder="2x NVIDIA H100 80GB HBM3" />
					<Segmented bind:value={spec.inventory.throughputClass} options={[{ value: '', label: 'Unset' }, { value: 'device', label: 'Device' }, { value: 'offload', label: 'Offload' }, { value: 'host', label: 'Host' }]} label="Throughput class" />
				</FieldRow>
				<MultiSelect bind:value={spec.inventory.modalities} options={modalities} label="Modalities" />
			{/if}
			{#if spec.metrics}
				<FieldRow>
					<Text bind:value={spec.metrics.metricsPath} label="Metrics path" mono placeholder="/metrics" />
					<NumberField bind:value={spec.metrics.metricsPort} min={0} max={65535} label="Metrics port" emptyZero placeholder="Default" info="The endpoint port when unset" />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.healthPath} label="Health path" mono placeholder="/health" />
					<Text bind:value={spec.metrics.slotsPath} label="Slots path" mono />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.queuedRequests} label="Queued requests gauge" mono />
					<KeyValues bind:value={spec.metrics.queuedRequestsMatch} label="Queued requests labels" />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.runningRequests} label="Running requests gauge" mono />
					<KeyValues bind:value={spec.metrics.runningRequestsMatch} label="Running requests labels" />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.kvCacheUtilization} label="KV utilization gauge" mono />
					<KeyValues bind:value={spec.metrics.kvCacheUtilizationMatch} label="KV utilization labels" />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.cacheConfigInfo} label="Cache config gauge" mono />
					<Text bind:value={spec.metrics.replicaLabel} label="Replica label" mono />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.blockSizeLabel} label="Block size label" mono />
					<Text bind:value={spec.metrics.numBlocksLabel} label="Block count label" mono />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.blockSize} label="Block size gauge" mono />
					<KeyValues bind:value={spec.metrics.blockSizeMatch} label="Block size labels" />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.numBlocks} label="Block count gauge" mono />
					<KeyValues bind:value={spec.metrics.numBlocksMatch} label="Block count labels" />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.loraInfo} label="LoRA info gauge" mono />
					<Text bind:value={spec.metrics.maxLoraLabel} label="Max LoRA label" mono />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.runningAdaptersLabel} label="Running adapters label" mono />
					<Text bind:value={spec.metrics.waitingAdaptersLabel} label="Waiting adapters label" mono />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.adaptersPath} label="Adapters path" mono />
					<Segmented bind:value={spec.metrics.adaptersFormat} options={[{ value: '', label: 'Unset' }, { value: 'llamacpp', label: 'llama.cpp' }, { value: 'openai', label: 'OpenAI' }]} label="Adapters format" />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.maxLoraPath} label="Max LoRA path" mono />
					<Text bind:value={spec.metrics.maxLoraField} label="Max LoRA field" mono />
				</FieldRow>
				<Switch bind:checked={spec.metrics.staticCache} label="Static cache" info="Every running request holds a full length cache" />
			{/if}
		{/snippet}
	</FormSection>
	<FormSection title="Static endpoints" info="Servers the pool reaches that nebu does not manage, instances nebu launches join on their own">
		{#if spec.staticEndpoints.length}
			<div class="endpoints">
				{#each spec.staticEndpoints as ep, i (i)}
					<div class="endpoint">
						<span class="address"><Text bind:value={ep.address} label={i === 0 ? 'Address' : undefined} placeholder="host:port" mono required /></span>
						<span class="role"><Text bind:value={ep.role} label={i === 0 ? 'Role' : undefined} max={32} placeholder="Any" /></span>
						<span class="small"><NumberField bind:value={ep.rank} min={0} label={i === 0 ? 'Rank' : undefined} /></span>
						<span class="small"><NumberField bind:value={ep.kvCapacity} min={0} label={i === 0 ? 'KV tokens' : undefined} emptyZero placeholder="Default" /></span>
						<span class="trail"><NeoButton text aria-label="Remove endpoint" title="Remove endpoint" onclick={() => (spec.staticEndpoints = spec.staticEndpoints.filter((_, j) => j !== i))}>{#snippet icon()}<XIcon size={18} />{/snippet}</NeoButton></span>
					</div>
				{/each}
			</div>
		{/if}
		<span class="add"><NeoButton elevation={2} onclick={addEndpoint}>{#snippet icon()}<PlusIcon size={18} />{/snippet}Endpoint</NeoButton></span>
	</FormSection>
</Stack>
<SaveBar {dirty} always={!editing} {saving} {error} message={problem} label={editing ? 'Save' : 'Create'} disabled={problem !== undefined} onsave={save} ondiscard={onclose} />

<style>
	.endpoints {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
	}
	/* Address and role share what the fixed rank, capacity and remove columns leave, wrapping in a narrow drawer */
	.endpoint {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-2) var(--nebu-space-3);
		align-items: flex-end;
		min-width: 0;
	}
	.address {
		display: flex;
		flex: 3 1 12rem;
		min-width: 0;
	}
	.role {
		display: flex;
		flex: 1 1 7rem;
		min-width: 0;
	}
	.small {
		display: flex;
		flex: 0 0 8.5rem;
		min-width: 0;
	}
	.endpoint > span > :global(*) {
		flex: 1 1 auto;
		min-width: 0;
	}
	.endpoint > .trail {
		margin-left: auto;
	}
	.add {
		display: inline-flex;
		align-self: flex-start;
	}
</style>
