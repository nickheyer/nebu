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
	const complete = $derived(id.trim() !== '' && spec.model.trim() !== '' && portNumbers.length > 0 && portNumbers.every((n) => Number.isInteger(n) && n >= 1 && n <= 65535) && spec.staticEndpoints.every((e) => e.address.trim() !== ''));

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

<PanelHeader title={editing ? id : 'New pool'} {onclose} />
<Stack gap={24}>
	<FormSection title="Pool">
		<FieldRow>
			<Text bind:value={id} label="Name" info="Fixed once created" required readonly={editing} max={64} />
			<Text bind:value={spec.model} label="Model" required max={128} />
		</FieldRow>
		<FieldRow>
			<Select value={spec.runtimeId || undefined} options={runtimes} label="Runtime" clearable onchange={(v) => (spec.runtimeId = v ?? '')} />
			<Text bind:value={spec.accelerator} label="Accelerator" info="Such as cpu" max={64} />
		</FieldRow>
		<KeyValues bind:value={spec.selector} label="Selector" info="Labels an endpoint must carry to join" />
		<FieldRow>
			<Chips bind:value={ports} label="Ports" numeric max={8} />
			<NumberField bind:value={spec.concurrency} min={0} label="Concurrency" info="Requests one endpoint runs at once when it reports no capacity" emptyZero />
		</FieldRow>
		<FieldRow>
			<Segmented bind:value={spec.appProtocol} options={[{ value: AppProtocol.HTTP, label: 'HTTP' }, { value: AppProtocol.H2C, label: 'h2c' }, { value: AppProtocol.GRPC, label: 'gRPC' }]} label="Protocol" />
			<Segmented bind:value={spec.failureMode} options={[{ value: FailureMode.FAIL_CLOSE, label: 'Reject' }, { value: FailureMode.FAIL_OPEN, label: 'Let through' }]} label="If the scheduler fails" info="What happens to a request when the scheduler cannot answer" />
		</FieldRow>
		<FieldRow>
			<Select value={spec.schedulerConfig || undefined} options={configs} label="Scheduler" clearable onchange={(v) => (spec.schedulerConfig = v ?? '')} />
			<Switch bind:checked={spec.export} label="Export" info="Offers the pool to every mesh member" top />
		</FieldRow>
		<FieldRow>
			<Switch bind:checked={spec.tokensInOut} label="Send token ids" info="The gateway turns prompts into token ids before sending them" />
			<span></span>
		</FieldRow>
		{#snippet advanced()}
			{#if spec.inventory}
				<FieldRow>
					<NumberField bind:value={spec.inventory.contextLength} min={0} label="Context length" unit="tokens" emptyZero />
					<Text bind:value={spec.inventory.quantization} label="Quantization" max={32} />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.inventory.accelerator} label="Launch accelerator" max={64} />
					<Segmented bind:value={() => spec.inventory?.throughputClass || undefined, (v) => (spec.inventory!.throughputClass = v ?? '')} options={[{ value: 'device', label: 'Device' }, { value: 'offload', label: 'Offload' }, { value: 'host', label: 'Host' }]} label="Throughput class" info="Whether the model runs on device, offloaded or on the host" clearable />
				</FieldRow>
				<MultiSelect bind:value={spec.inventory.modalities} options={modalities} label="Modalities" />
			{/if}
			{#if spec.metrics}
				<FieldRow>
					<Text bind:value={spec.metrics.metricsPath} label="Metrics path" info="Where the runtime serves Prometheus metrics" mono />
					<NumberField bind:value={spec.metrics.metricsPort} min={0} max={65535} label="Metrics port" info="Port for the metrics path when apart from the endpoint port" emptyZero fallback={portNumbers[0]} />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.healthPath} label="Health path" info="Readiness endpoint" mono />
					<Text bind:value={spec.metrics.slotsPath} label="Slots path" info="JSON slot listing KV use is summed from when there is no KV gauge" mono />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.queuedRequests} label="Queued requests gauge" info="Gauge counting waiting requests" mono />
					<KeyValues bind:value={spec.metrics.queuedRequestsMatch} label="Queued requests labels" info="Labels a series must match to count" />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.runningRequests} label="Running requests gauge" info="Gauge counting running requests" mono />
					<KeyValues bind:value={spec.metrics.runningRequestsMatch} label="Running requests labels" info="Labels a series must match to count" />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.kvCacheUtilization} label="KV utilization gauge" info="Gauge of KV cache in use" mono />
					<KeyValues bind:value={spec.metrics.kvCacheUtilizationMatch} label="KV utilization labels" info="Labels a series must match to count" />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.cacheConfigInfo} label="Cache config gauge" info="Info gauge carrying block size and block count" mono />
					<Text bind:value={spec.metrics.replicaLabel} label="Replica label" info="Label naming the scheduler replica, series sharing a value counted once" mono />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.blockSizeLabel} label="Block size label" info="Label on the cache config gauge holding block size" mono />
					<Text bind:value={spec.metrics.numBlocksLabel} label="Block count label" info="Label on the cache config gauge holding total blocks" mono />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.blockSize} label="Block size gauge" info="Gauge holding block size when there is no cache config gauge" mono />
					<KeyValues bind:value={spec.metrics.blockSizeMatch} label="Block size labels" info="Labels a series must match to count" />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.numBlocks} label="Block count gauge" info="Gauge holding total blocks when there is no cache config gauge" mono />
					<KeyValues bind:value={spec.metrics.numBlocksMatch} label="Block count labels" info="Labels a series must match to count" />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.loraInfo} label="LoRA info gauge" info="Info gauge listing loaded adapters" mono />
					<Text bind:value={spec.metrics.maxLoraLabel} label="Max LoRA label" info="Label on the LoRA info gauge holding adapters per batch" mono />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.runningAdaptersLabel} label="Running adapters label" info="Label on the LoRA info gauge listing running adapters" mono />
					<Text bind:value={spec.metrics.waitingAdaptersLabel} label="Waiting adapters label" info="Label on the LoRA info gauge listing waiting adapters" mono />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.adaptersPath} label="Adapters path" info="JSON listing of loaded adapters when there is no LoRA info gauge" mono />
					<Segmented bind:value={() => spec.metrics?.adaptersFormat || undefined, (v) => (spec.metrics!.adaptersFormat = v ?? '')} options={[{ value: 'llamacpp', label: 'llama.cpp' }, { value: 'openai', label: 'OpenAI' }]} label="Adapters format" info="Layout of the adapters listing" clearable />
				</FieldRow>
				<FieldRow>
					<Text bind:value={spec.metrics.maxLoraPath} label="Max LoRA path" info="JSON document holding adapters per batch" mono />
					<Text bind:value={spec.metrics.maxLoraField} label="Max LoRA field" info="Field in that document" mono />
				</FieldRow>
				<Text bind:value={spec.metrics.workCounter} label="Work counter" info="Counter that only grows as requests are served" mono />
				<Switch bind:checked={spec.metrics.staticCache} label="Static cache" info="Every running request holds a full length cache" />
			{/if}
		{/snippet}
	</FormSection>
	<FormSection title="Static endpoints">
		{#if spec.staticEndpoints.length}
			<div class="endpoints">
				{#each spec.staticEndpoints as ep, i (i)}
					<div class="endpoint">
						<span class="address"><Text bind:value={ep.address} label={i === 0 ? 'Address' : undefined} info={i === 0 ? 'host:port' : undefined} mono required /></span>
						<span class="role"><Text bind:value={ep.role} label={i === 0 ? 'Role' : undefined} info={i === 0 ? 'The endpoint part, such as prefill or decode' : undefined} max={32} /></span>
						<span class="small"><NumberField bind:value={ep.rank} min={0} label={i === 0 ? 'Rank' : undefined} info={i === 0 ? 'Order among endpoints sharing a role' : undefined} /></span>
						<span class="small"><NumberField bind:value={ep.kvCapacity} min={0} label={i === 0 ? 'Cache tokens' : undefined} info={i === 0 ? 'Across the whole server' : undefined} emptyZero /></span>
						<span class="trail"><NeoButton text aria-label="Remove endpoint" title="Remove endpoint" onclick={() => (spec.staticEndpoints = spec.staticEndpoints.filter((_, j) => j !== i))}>{#snippet icon()}<XIcon size={18} />{/snippet}</NeoButton></span>
					</div>
				{/each}
			</div>
		{/if}
		<span class="add"><NeoButton elevation={2} onclick={addEndpoint}>{#snippet icon()}<PlusIcon size={18} />{/snippet}Endpoint</NeoButton></span>
	</FormSection>
</Stack>
{#snippet deleteButton()}
	<Confirm label="Delete" question="Delete {id}?" danger placement="top-start" onconfirm={remove} />
{/snippet}

<SaveBar {dirty} always={!editing} {saving} {error} label={editing ? 'Save' : 'Create'} disabled={!complete} leading={editing ? deleteButton : undefined} onsave={save} ondiscard={onclose} />

<style>
	.endpoints {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
	}
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
