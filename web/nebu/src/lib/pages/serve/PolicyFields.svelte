<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { onMount } from 'svelte';
	import { reason, rpc } from '#lib/api/transport.js';
	import { PolicySchema, ProfileSchema, type Policy, type Profile } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { ToolServerState, type ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import { live } from '#lib/live/events.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';

	let { policy = $bindable(create(PolicySchema)), profile = $bindable(create(ProfileSchema)) }: { policy?: Policy; profile?: Profile } = $props();

	let remote = $state<ToolServerStatus[]>([]);
	let width = $state(0);
	// Two durations share a row only once each has room for its number and its units
	const pairDurations = $derived(width >= 720);

	// Adds the tool servers other mesh members run
	onMount(async () => {
		if (!live.inMesh) return;
		try {
			remote = (await rpc.tools.listToolServers({ mesh: true })).servers.filter((s) => s.nodeId !== '');
		} catch (err) {
			toast.error('Mesh tool servers failed to list', reason(err));
		}
	});

	const servers = $derived(
		[...live.toolServers.values(), ...remote].map((t) => ({
			value: t.ref || t.server?.name || '',
			label: t.ref || t.server?.name || '',
			description: t.nodeId ? `on ${live.nodes.get(t.nodeId)?.name ?? t.nodeId}` : undefined,
			disabled: t.state === ToolServerState.FAILED,
			tip: t.state === ToolServerState.FAILED ? t.error : undefined
		}))
	);
</script>

{#snippet requestTimeout()}
	<Duration bind:value={policy.requestTimeoutMs} base="ms" max={86_400_000} label="Request timeout" info="The whole exchange, first byte in to last byte out" emptyZero placeholder="Default" />
{/snippet}

{#snippet upstreamTimeout()}
	<Duration bind:value={policy.upstreamTimeoutMs} base="ms" max={86_400_000} label="Upstream timeout" info="How long the runtime may take to start answering" emptyZero placeholder="Default" />
{/snippet}

<div class="policy" bind:clientWidth={width}>
	<FormSection title="Policy" info="Empty fields take the gateway default, burst being the requests a quiet route absorbs at once">
		<FieldRow>
			<NumberField bind:value={policy.maxInFlight} min={0} max={1_000_000} label="Max in flight" emptyZero placeholder="Default" />
			<NumberField bind:value={policy.requestsPerSecond} min={0} step={0.5} label="Requests per second" emptyZero placeholder="Default" />
			<NumberField bind:value={policy.burst} min={0} max={1_000_000} label="Burst" emptyZero placeholder="Default" />
		</FieldRow>
		{#if pairDurations}
			<FieldRow>
				{@render requestTimeout()}
				{@render upstreamTimeout()}
			</FieldRow>
		{:else}
			{@render requestTimeout()}
			{@render upstreamTimeout()}
		{/if}
	</FormSection>
	<FormSection title="Tools">
		<Checklist bind:value={profile.toolServers} options={servers} label="Tool servers" empty="None set up in Settings" />
		<Switch bind:checked={profile.mutatingTools} label="Mutating tools" info="Offers tools that change what they touch, read only tools alone otherwise" />
	</FormSection>
</div>

<style>
	.policy {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-5);
		min-width: 0;
	}
</style>
