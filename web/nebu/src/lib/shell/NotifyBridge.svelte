<script lang="ts">
	import type { NeoNotificationStackService } from '#lib/neo/floating/notification/index.js';
	import { bindNotifications } from './notify.svelte.js';

	let { services }: { services: ReadonlyMap<string | undefined, NeoNotificationStackService> } = $props();

	$effect(() => {
		const first = services.values().next().value;
		if (first) bindNotifications(first);
	});
</script>
