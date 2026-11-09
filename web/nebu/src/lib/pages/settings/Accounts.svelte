<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import type { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { ago, enumLabel, when } from '#lib/format.js';
	import { Role, RoleSchema, type User } from '#lib/proto/nebu/v1/auth_pb.js';
	import type { HeaderTab } from '#lib/shell/chrome.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import UsersIcon from 'phosphor-svelte/lib/UsersIcon';

	let { tabs, users, off }: { tabs: HeaderTab[]; users: ListQuery<User, Record<string, never>>; off: boolean } = $props();

	const roles = [
		{ value: Role.ADMIN, label: 'Admin' },
		{ value: Role.OPERATOR, label: 'Operator' },
		{ value: Role.INFERENCE, label: 'Inference' }
	];
	const sso = $derived(session.status?.sso?.name ?? '');

	let addOpen = $state(false);
	let username = $state('');
	let password = $state('');
	let role = $state<Role>(Role.OPERATOR);
	let addBusy = $state(false);
	let addError = $state('');

	let passwordOpen = $state(false);
	let target = $state<User | undefined>(undefined);
	let fresh = $state('');
	let passwordBusy = $state(false);
	let passwordError = $state('');

	function openAdd() {
		username = '';
		password = '';
		role = Role.OPERATOR;
		addError = '';
		addOpen = true;
	}

	async function add() {
		addBusy = true;
		addError = '';
		try {
			await rpc.auth.createUser({ username: username.trim(), password, role });
			toast.success(`Created ${username.trim()}`);
			addOpen = false;
			await users.load({});
		} catch (err) {
			addError = reason(err);
		} finally {
			addBusy = false;
		}
	}

	function openPassword(u: User) {
		target = u;
		fresh = '';
		passwordError = '';
		passwordOpen = true;
	}

	async function setPassword() {
		if (!target) return;
		passwordBusy = true;
		passwordError = '';
		try {
			await rpc.auth.setPassword({ username: target.username, password: fresh, currentPassword: '' });
			toast.success(`Password set for ${target.username}`);
			passwordOpen = false;
		} catch (err) {
			passwordError = reason(err);
		} finally {
			passwordBusy = false;
		}
	}

	async function remove(u: User) {
		try {
			await rpc.auth.deleteUser({ username: u.username });
			toast.success(`Deleted ${u.username}`);
			await users.load({});
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}

	function roleLamp(r: Role): 'primary' | 'muted' {
		return r === Role.ADMIN ? 'primary' : 'muted';
	}

	function own(u: User): boolean {
		return u.username === session.user?.name || u.username === session.user?.subject;
	}
</script>

<PageHeader {tabs} active="accounts" info="Local sign ins and the role each one holds" />

<Card title="Accounts" flush>
	{#snippet tools()}
		{#if !off}<NeoButton class="primary" elevation={2} onclick={openAdd}>Add account</NeoButton>{/if}
	{/snippet}
	{#if off}
		<Empty icon={UsersIcon} text={sso ? `Managed by ${sso}` : 'Authentication off'} />
	{:else if !users.settled}
		<Skeleton rows={3} dot={false} secondary="10ch" facts={1} action={false} />
	{:else if users.error}
		<Empty text={users.error} action="Retry" onclick={() => users.refresh()} />
	{:else if users.items.length === 0}
		<Empty icon={UsersIcon} text="No accounts" action="Add account" onclick={openAdd} />
	{:else}
		<List head={{ primary: 'Username', facts: ['Created'] }}>
			{#each users.items as u (u.id)}
				<ListRow
					primary={u.username}
					facts={[{ value: ago(u.createdAt, clockTick.now), tip: when(u.createdAt) }]}
					menu={[
						{ label: 'Set password', onclick: () => openPassword(u) },
						{ label: 'Delete', danger: true, divider: true, disabled: own(u), description: own(u) ? 'signed in' : undefined, confirm: `Delete ${u.username}?`, onclick: () => remove(u) }
					]}
				>
					{#snippet badges()}<StatePill label={enumLabel(RoleSchema, u.role)} lamp={roleLamp(u.role)} />{/snippet}
				</ListRow>
			{/each}
		</List>
		<LoadMore next={users.next} loading={users.loading} onmore={() => users.more()} />
	{/if}
</Card>

<Dialog bind:open={addOpen} title="New account" confirm="Create" busy={addBusy} disabled={!username.trim() || !password} onconfirm={add}>
	<Text bind:value={username} label="Username" max={64} required autocomplete="off" />
	<Secret bind:value={password} label="Password" required autocomplete="new-password" />
	<Segmented bind:value={role} options={roles} label="Role" info="Admin does everything, operator runs and edits, inference only sends requests" />
	{#snippet footer()}
		{#if addError}<span class="error-text">{addError}</span>{/if}
	{/snippet}
</Dialog>

<Dialog bind:open={passwordOpen} title="Set password" confirm="Set" busy={passwordBusy} disabled={!fresh} onconfirm={setPassword}>
	{#if target}<Text value={target.username} label="Username" readonly />{/if}
	<Secret bind:value={fresh} label="New password" required autocomplete="new-password" />
	{#snippet footer()}
		{#if passwordError}<span class="error-text">{passwordError}</span>{/if}
	{/snippet}
</Dialog>
