<script lang="ts">
	import { goto } from '$app/navigation';
	import { reason, rpc } from '#lib/api/transport.js';
	import { InstanceState, type Instance } from '#lib/proto/nebu/v1/instance_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import type { MenuItem } from '#lib/ui/Menu.svelte';
	import { instanceLamp, instanceShape } from '#lib/ui/status.js';
	import { modelPhrase, terminal } from '#lib/pages/serve/serve.js';

	let { instance, ondismiss, selected = false, slim = false }: { instance: Instance; ondismiss: (id: string) => void; selected?: boolean; slim?: boolean } = $props();

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
		const items: MenuItem[] = [{ label: 'Logs', href: `/serve/${encodeURIComponent(instance.id)}?tab=logs` }];
		if (done) items.push({ label: 'Dismiss', onclick: () => ondismiss(instance.id), divider: true });
		return items;
	});
</script>

<ListRow
	lamp={instanceLamp(instance.state)}
	shape={instanceShape(instance.state)}
	primary={instance.name}
	secondary={slim ? undefined : phrase}
	secondaryKind={instance.state === InstanceState.FAILED ? 'message' : 'repo'}
	secondaryColor={instance.state === InstanceState.FAILED ? 'error' : 'muted'}
	{menu}
	{selected}
	onclick={() => goto(`/serve/${encodeURIComponent(instance.id)}`)}
>
	{#snippet action()}
		{#if !done}
			<Confirm label="Stop" question="Stop {instance.name}?" danger onconfirm={stop} />
		{/if}
	{/snippet}
</ListRow>
