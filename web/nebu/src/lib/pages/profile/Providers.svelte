<script lang="ts">
	import { onMount } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ago, when } from '#lib/format.js';
	import { CredentialKind, type ProviderCredential, type ProviderSignIn } from '#lib/proto/nebu/v1/auth_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { catalog } from '#lib/state/catalog.svelte.js';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import PlugsConnectedIcon from 'phosphor-svelte/lib/PlugsConnectedIcon';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import SignInIcon from 'phosphor-svelte/lib/SignInIcon';

	// The caller's own credentials for providers serving models
	const credentials = new ListQuery<ProviderCredential, Record<string, never>>(async (_p, cursor, limit, signal) => {
		const res = await rpc.auth.listCredentials({ cursor, limit }, { signal });
		return { items: res.credentials, next: res.nextCursor };
	}, []);

	onMount(() => {
		void credentials.load({});
		void catalog.load();
	});

	// The sources taking each kind of credential
	const providers = $derived(catalog.sources.filter((s) => s.capabilities?.credentials.length));
	const taking = (kind: CredentialKind) => providers.filter((s) => s.capabilities?.credentials.includes(kind)).map((s) => ({ value: s.source?.id ?? '', label: s.source?.name || s.source?.id || '' }));
	const keyed = $derived(taking(CredentialKind.KEY));
	const signed = $derived(taking(CredentialKind.SIGN_IN));
	const sourceName = (id: string) => providers.find((s) => s.source?.id === id)?.source?.name || id;

	let keyOpen = $state(false);
	// The credential whose key is replaced, none for a new one
	let editing = $state<ProviderCredential | undefined>(undefined);
	let sourceId = $state('');
	let name = $state('');
	let secret = $state('');
	let busy = $state(false);
	let error = $state('');

	function openKey(c?: ProviderCredential) {
		editing = c;
		sourceId = c?.sourceId ?? keyed[0]?.value ?? '';
		name = c?.name ?? '';
		secret = '';
		error = '';
		keyOpen = true;
	}

	// Lists again, and the sources, whose credential state follows
	async function changed() {
		await credentials.load({});
		void catalog.refresh();
	}

	async function saveKey() {
		busy = true;
		error = '';
		try {
			await rpc.auth.putCredential({ id: editing?.id ?? '', sourceId: editing ? '' : sourceId, name: name.trim(), secret: secret.trim() });
			keyOpen = false;
			toast.success(editing ? `Updated ${name || editing.name}` : `Key kept for ${sourceName(sourceId)}`);
			await changed();
		} catch (err) {
			error = reason(err);
		} finally {
			busy = false;
			secret = '';
		}
	}

	async function remove(c: ProviderCredential) {
		try {
			await rpc.auth.deleteCredential({ id: c.id });
			toast.success(`Removed ${c.name}`);
			await changed();
		} catch (err) {
			toast.error('Remove failed', reason(err));
		}
	}

	let signOpen = $state(false);
	let pending = $state<ProviderSignIn | undefined>(undefined);
	// The address the browser landed on after approving, copied back for a sign-in by redirect
	let landed = $state('');
	let waiting: AbortController | undefined;

	function openSignIn() {
		sourceId = signed[0]?.value ?? '';
		name = '';
		error = '';
		landed = '';
		pending = undefined;
		signOpen = true;
	}

	// Starts the sign-in, then for a device code waits for the person to approve it
	async function signIn() {
		if (pending) return finish(landed.trim());
		busy = true;
		error = '';
		try {
			const res = await rpc.auth.startSignIn({ sourceId, name: name.trim() });
			pending = res.signIn;
			if (pending?.code) await finish('');
		} catch (err) {
			error = reason(err);
		} finally {
			busy = false;
		}
	}

	async function finish(callback: string) {
		busy = true;
		error = '';
		waiting = new AbortController();
		try {
			await rpc.auth.finishSignIn({ id: pending?.id ?? '', callback }, { signal: waiting.signal });
			signOpen = false;
			toast.success(`Signed in to ${sourceName(sourceId)}`);
			await changed();
		} catch (err) {
			if (!waiting.signal.aborted) error = reason(err);
		} finally {
			busy = false;
		}
	}

	function closeSignIn() {
		waiting?.abort();
		pending = undefined;
	}
</script>

<Card title="Providers" flush>
	{#snippet tools()}
		{#if signed.length}<NeoButton elevation={2} onclick={openSignIn}>{#snippet icon()}<SignInIcon size={16} />{/snippet}Sign in</NeoButton>{/if}
		{#if keyed.length}<NeoButton class="primary" elevation={2} onclick={() => openKey()}>{#snippet icon()}<PlusIcon size={16} />{/snippet}Key</NeoButton>{/if}
	{/snippet}
	{#if !credentials.settled}
		<Skeleton rows={2} dot={false} secondary="10ch" action={false} />
	{:else if credentials.error}
		<Empty text={credentials.error} action="Retry" onclick={() => credentials.refresh()} />
	{:else if credentials.items.length === 0}
		<Empty icon={PlugsConnectedIcon} text="No credentials" hint="Requests to a provider's models carry your own" />
	{:else}
		<List head={{ primary: 'Name', secondary: 'Source', instrument: 'Kind', facts: ['Expires'] }}>
			{#each credentials.items as c (c.id)}
				<ListRow
					primary={c.name}
					secondary={sourceName(c.sourceId)}
					secondaryWidth="16ch"
					facts={[c.expiresAt ? { value: ago(c.expiresAt, clockTick.now), tip: `access lapses ${when(c.expiresAt)}, refreshed before` } : { value: '', tip: `updated ${when(c.updatedAt)}` }]}
					menu={[
						...(c.kind === CredentialKind.KEY ? [{ label: 'Replace key', onclick: () => openKey(c) }] : []),
						{ label: 'Remove', danger: true, confirm: `Remove ${c.name}?`, onclick: () => remove(c) }
					]}
				>
					{#snippet instrument()}<span class="secondary">{c.kind === CredentialKind.SIGN_IN ? 'Sign-in' : 'Key'}{c.position > 1 ? ` · ${c.position}` : ''}</span>{/snippet}
				</ListRow>
			{/each}
		</List>
		<LoadMore next={credentials.next} loading={credentials.loading} onmore={() => credentials.more()} />
	{/if}
</Card>

<Dialog bind:open={keyOpen} title={editing ? 'Replace key' : 'New key'} confirm="Save" {busy} needs={!editing && !sourceId ? 'Pick a source' : !secret.trim() ? 'Enter the key' : ''} onconfirm={saveKey} onclose={() => (secret = '')}>
	{#if !editing}<Select bind:value={sourceId} options={keyed} label="Source" required />{/if}
	<Text bind:value={name} label="Name" max={64} placeholder={editing ? editing.name : sourceName(sourceId)} />
	<Secret bind:value={secret} label="Key" required />
	{#snippet footer()}
		{#if error}<span class="error-text">{error}</span>{/if}
	{/snippet}
</Dialog>

<Dialog bind:open={signOpen} title="Sign in" confirm={pending?.code ? undefined : pending ? 'Finish' : 'Continue'} {busy} needs={!sourceId ? 'Pick a source' : pending && !pending.code && !landed.trim() ? 'Paste the address' : ''} onconfirm={signIn} onclose={closeSignIn}>
	{#if pending?.code}
		<p class="secondary">Open the page, enter the code, and approve</p>
		<a href={pending.url} target="_blank" rel="noreferrer">{pending.url}</a>
		<div class="code">
			<Screen padding={8}><span class="value">{pending.code}</span></Screen>
			<CopyButton text={pending.code} />
		</div>
	{:else if pending}
		<p class="secondary">Open the page and approve, then paste the address it lands on, or the code it shows</p>
		<a href={pending.url} target="_blank" rel="noreferrer">Open sign-in page</a>
		<Text bind:value={landed} label="Address or code" required />
	{:else}
		<Select bind:value={sourceId} options={signed} label="Source" required />
		<Text bind:value={name} label="Name" max={64} placeholder={sourceName(sourceId)} />
	{/if}
	{#snippet footer()}
		{#if error}<span class="error-text">{error}</span>{/if}
	{/snippet}
</Dialog>

<style>
	.code {
		display: flex;
		gap: var(--nebu-space-2);
		align-items: center;
		min-width: 0;
	}
	.code > :global(.screen) {
		flex: 1 1 auto;
	}
	.value {
		overflow-wrap: anywhere;
	}
</style>
