<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import type { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { ago, capitalize, enumLabel, when } from '#lib/format.js';
	import { Scope, ScopeSchema, type ApiToken } from '#lib/proto/nebu/v1/auth_pb.js';
	import type { HeaderTab } from '#lib/shell/chrome.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import KeyIcon from 'phosphor-svelte/lib/KeyIcon';

	let { tabs, tokens }: { tabs: HeaderTab[]; tokens: ListQuery<ApiToken, Record<string, never>> } = $props();

	const auth = $derived(session.status?.enabled === true);
	// Scopes no wider than the caller's own role
	const roleScopes = $derived(session.role === 'admin' ? [Scope.INFERENCE, Scope.READ, Scope.OPERATE, Scope.ADMIN] : session.role === 'operator' ? [Scope.INFERENCE, Scope.READ, Scope.OPERATE] : [Scope.INFERENCE]);

	let newOpen = $state(false);
	let name = $state('');
	let scopes = $state<Scope[]>([Scope.INFERENCE]);
	let busy = $state(false);
	let error = $state('');
	let secret = $state('');
	let secretOpen = $state(false);

	function openNew() {
		name = '';
		scopes = [Scope.INFERENCE];
		error = '';
		newOpen = true;
	}

	async function create() {
		busy = true;
		error = '';
		try {
			const res = await rpc.auth.createToken({ name: name.trim(), scopes });
			if (!res.token?.secret) throw new Error('the daemon answered without the secret');
			secret = res.token.secret;
			newOpen = false;
			secretOpen = true;
			await tokens.load({});
		} catch (err) {
			error = reason(err);
		} finally {
			busy = false;
		}
	}

	async function revoke(t: ApiToken) {
		try {
			await rpc.auth.deleteToken({ id: t.id });
			toast.success(`Revoked ${t.name}`);
			await tokens.load({});
		} catch (err) {
			toast.error('Revoke failed', reason(err));
		}
	}

	function scopeLabel(s: Scope): string {
		return enumLabel(ScopeSchema, s);
	}
</script>

<PageHeader {tabs} active="tokens">
	{#snippet actions()}
		{#if auth}<NeoButton class="primary" elevation={2} onclick={openNew}>New token</NeoButton>{/if}
	{/snippet}
</PageHeader>

<Card title="API tokens" count={tabs.length === 0 && tokens.settled ? tokens.items.length : undefined} info="Bearer tokens for clients that call the daemon and the gateway without signing in" flush>
	{#if !auth}
		<Empty icon={KeyIcon} text="Authentication is off" hint="Tokens appear here once the daemon runs with authentication on" />
	{:else if !tokens.settled}
		<Skeleton rows={3} dot={false} secondary="10ch" action={false} />
	{:else if tokens.error}
		<Empty text={tokens.error} action="Retry" onclick={() => tokens.refresh()} />
	{:else if tokens.items.length === 0}
		<Empty icon={KeyIcon} text="No tokens" hint="A token lets a script or client act as you within the scopes you give it" action="New token" onclick={openNew} />
	{:else}
		<List head={{ primary: 'Name', secondary: 'Prefix', instrument: 'Scopes', facts: ['Last used'] }}>
			{#each tokens.items as t (t.id)}
				<ListRow
					primary={t.name}
					secondary={t.prefix}
					secondaryKind="id"
					secondaryWidth="12ch"
					facts={[t.lastUsedAt ? { value: ago(t.lastUsedAt, clockTick.now), tip: `last used ${when(t.lastUsedAt)}` } : { value: 'never', tip: `created ${when(t.createdAt)}` }]}
					menu={[{ label: 'Revoke', danger: true, confirm: `Revoke ${t.name}?`, onclick: () => revoke(t) }]}
				>
					{#snippet instrument()}
						<span class="scopes">
							{#each t.scopes as s (s)}<StatePill label={scopeLabel(s)} lamp={s === Scope.ADMIN ? 'primary' : 'muted'} />{/each}
						</span>
					{/snippet}
				</ListRow>
			{/each}
		</List>
		<LoadMore next={tokens.next} loading={tokens.loading} onmore={() => tokens.more()} />
	{/if}
</Card>

<Dialog bind:open={newOpen} title="New token" confirm="Create" {busy} disabled={!name.trim() || scopes.length === 0} onconfirm={create}>
	<Text bind:value={name} label="Name" min={1} placeholder="CI runner" />
	<Checklist bind:value={scopes} options={roleScopes.map((s) => ({ value: s, label: capitalize(scopeLabel(s)) }))} label="Scopes" />
	{#snippet footer()}
		{#if error}<span class="error-text">{error}</span>{/if}
	{/snippet}
</Dialog>

<Dialog bind:open={secretOpen} title="Token created" onclose={() => (secret = '')}>
	<p class="secondary">Copy it now. The secret is shown once and cannot be read again.</p>
	<div class="secret">
		<Screen padding={8}><span class="value">{secret}</span></Screen>
		<CopyButton text={secret} />
	</div>
</Dialog>

<style>
	.scopes {
		display: inline-flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-1);
		align-items: center;
	}
	.secret {
		display: flex;
		gap: var(--nebu-space-2);
		align-items: center;
		min-width: 0;
	}
	.secret > :global(.screen) {
		flex: 1 1 auto;
	}
	.value {
		overflow-wrap: anywhere;
	}
</style>
