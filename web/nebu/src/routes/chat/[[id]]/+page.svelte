<script lang="ts">
	import { page } from '$app/state';
	import Chat from '#lib/pages/chat/Chat.svelte';
</script>

<svelte:head><title>Chat</title></svelte:head>
<Chat id={page.params.id ?? ''} />
