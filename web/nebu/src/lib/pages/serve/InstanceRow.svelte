<script lang="ts">
	import { reason, rpc } from '#lib/api/transport.js';
	import { num } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { InstanceState, type Instance } from '#lib/proto/nebu/v1/instance_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import type { MenuItem } from '#lib/ui/Menu.svelte';
	import { instanceLamp, instanceShape } from '#lib/ui/status.js';
	import { instanceInFlight, modelPhrase, terminal } from '#lib/pages/serve/serve.js';

	let { instance, ondismiss }: { instance: Instance; ondismiss?: (id: string) => void } = $props();

	const inFlight = $derived(instanceInFlight(live, instance));
	const done = $derived(terminal(instance.state));
	const phrase = $derived(instance.state === InstanceState.FAILED && instance.error ? instance.error : modelPhrase(instance));

	async function stop() {
		try {
			await rpc.instances.stopInstance({ id: instance.id });
		} catch (err) {
			toast.error('Stop failed', reason(err));
		}
	}

	const menu = $derived.by((): MenuItem[] => {
		const items: MenuItem[] = [{ label: 'Logs', href: `/serve/${instance.id}?tab=logs` }, { label: 'Open', href: `/serve/${instance.id}` }];
		if (done && ondismiss) items.push({ label: 'Dismiss', onclick: () => ondismiss(instance.id), divider: true });
		return items;
	});
</script>

<ListRow
	lamp={instanceLamp(instance.state)}
	shape={instanceShape(instance.state)}
	primary={instance.name}
	secondary={phrase}
	secondaryKind={instance.state === InstanceState.FAILED ? 'message' : 'repo'}
	secondaryColor={instance.state === InstanceState.FAILED ? 'error' : 'muted'}
	facts={!done && inFlight !== undefined ? [{ value: `${num(inFlight)} in flight` }] : []}
	{menu}
	href="/serve/{instance.id}"
>
	{#snippet action()}
		{#if !done}
			<Confirm label="Stop" question="Stop {instance.name}?" danger onconfirm={stop} />
		{/if}
	{/snippet}
</ListRow>
