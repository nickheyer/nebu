<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { onMount } from 'svelte';
	import { reason, rpc } from '#lib/api/transport.js';
	import { PolicySchema, ProfileSchema, type Policy, type Profile } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { ToolServerState, type ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import { live } from '#lib/live/events.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { defaults } from '#lib/state/defaults.svelte.js';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import type { Option } from '#lib/ui/fields/types.js';
	import { burstFallback } from '#lib/pages/serve/serve.js';

	// A route's limits and tools, every empty limit showing the gateway's own value
	let { policy = $bindable(create(PolicySchema)), profile = $bindable(create(ProfileSchema)), disabled = false }: { policy?: Policy; profile?: Profile; disabled?: boolean } = $props();

	let listed = $state<ToolServerStatus[]>([]);
	const d = $derived(defaults.policy);

	// Reads every tool server this node and the mesh members run, page by page
	onMount(async () => {
		defaults.load();
		try {
			const all: ToolServerStatus[] = [];
			let cursor = '';
			do {
				const res = await rpc.tools.listToolServers({ mesh: live.inMesh, cursor, limit: 200 });
				all.push(...res.servers);
				cursor = res.nextCursor;
			} while (cursor);
			listed = all;
		} catch (err) {
			toast.error('Tool servers failed to list', reason(err));
		}
	});

	// Tool servers by ref, live local state winning, a named one nobody runs kept so it can be unchecked
	const servers = $derived.by((): Option<string>[] => {
		const byRef = new Map<string, ToolServerStatus>();
		for (const s of listed) byRef.set(s.ref || s.server?.name || '', s);
		for (const s of live.toolServers.values()) byRef.set(s.ref || s.server?.name || '', s);
		const options = [...byRef.entries()]
			.filter(([ref]) => ref !== '')
			.sort(([a], [b]) => a.localeCompare(b))
			.map(([ref, s]): Option<string> => ({
				value: ref,
				label: ref,
				description: s.nodeId ? (live.nodes.get(s.nodeId)?.name ?? s.nodeId) : undefined,
				disabled: disabled || (s.state === ToolServerState.FAILED && !profile.toolServers.includes(ref)),
				tip: s.state === ToolServerState.FAILED ? s.error : undefined
			}));
		for (const ref of profile.toolServers) if (!byRef.has(ref)) options.push({ value: ref, label: ref, description: 'missing', disabled });
		return options;
	});
</script>

<FormSection title="Limits">
	<FieldRow>
		<NumberField bind:value={policy.maxInFlight} min={0} max={1_000_000} label="Concurrent requests" emptyZero fallback={d?.maxInFlight || undefined} {disabled} />
		<NumberField bind:value={policy.requestsPerSecond} min={0} step={0.5} label="Requests per second" emptyZero fallback={d?.requestsPerSecond || undefined} {disabled} />
		<NumberField bind:value={policy.burst} min={0} max={1_000_000} label="Burst" info="Requests let through at once above the rate" emptyZero fallback={burstFallback(policy, d)} {disabled} />
	</FieldRow>
	<FieldRow wide>
		<Duration bind:value={policy.requestTimeoutMs} base="ms" max={86_400_000} label="Request timeout" emptyZero fallback={d?.requestTimeoutMs || undefined} {disabled} />
		<Duration bind:value={policy.upstreamTimeoutMs} base="ms" max={86_400_000} label="Runtime timeout" info="Longest wait for the runtime to answer" emptyZero fallback={d?.upstreamTimeoutMs || undefined} {disabled} />
	</FieldRow>
</FormSection>
{#if servers.length}
	<FormSection title="Tools">
		<Checklist bind:value={profile.toolServers} options={servers} />
		{#if profile.toolServers.length}
			<Switch bind:checked={profile.mutatingTools} label="Let tools make changes" {disabled} />
		{/if}
	</FormSection>
{/if}
