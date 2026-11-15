<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import type { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { ago, when } from '#lib/format.js';
	import { Role, type User } from '#lib/proto/nebu/v1/auth_pb.js';
	import { SignInSchema } from '#lib/proto/nebu/v1/settings_pb.js';
	import type { HeaderTab } from '#lib/shell/chrome.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import Columns from '#lib/ui/layout/Columns.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import { SectionDraft, SettingsForm } from '#lib/pages/settings/form.svelte.js';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import UsersIcon from 'phosphor-svelte/lib/UsersIcon';

	let { tabs, users, off }: { tabs: HeaderTab[]; users: ListQuery<User, Record<string, never>>; off: boolean } = $props();

	const roles = [
		{ value: Role.ADMIN, label: 'Admin', grants: 'Does everything, accounts and settings included' },
		{ value: Role.OPERATOR, label: 'Operator', grants: 'Runs and edits models, routes and bots' },
		{ value: Role.INFERENCE, label: 'Inference', grants: 'Only sends requests to the models being served' }
	];
	const sso = $derived(session.status?.sso?.name ?? '');
	const auth = $derived(session.status?.enabled === true);
	const form = new SettingsForm();
	const signIn = new SectionDraft(form, SignInSchema, (s) => s.signIn, (s, v) => (s.signIn = v), 'Sign in saved');
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

	let roleOpen = $state(false);
	let nextRole = $state<Role>(Role.OPERATOR);
	let roleBusy = $state(false);
	let roleError = $state('');

	function roleOf(r: Role) {
		return roles.find((o) => o.value === r);
	}

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

	function openRole(u: User) {
		target = u;
		nextRole = u.role;
		roleError = '';
		roleOpen = true;
	}

	// Gives the account the chosen role, which ends its sessions, the caller's own among them
	async function setRole() {
		if (!target) return;
		const u = target;
		roleBusy = true;
		roleError = '';
		try {
			await rpc.auth.setRole({ username: u.username, role: nextRole });
			toast.success(`${u.username} is now ${roleOf(nextRole)?.label.toLowerCase()}`);
			roleOpen = false;
			if (own(u)) await session.load();
			else await users.load({});
		} catch (err) {
			roleError = reason(err);
		} finally {
			roleBusy = false;
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

	function own(u: User): boolean {
		return u.username === session.user?.name || u.username === session.user?.subject;
	}
</script>

<PageHeader {tabs} active="accounts" />

{#snippet accountsCard()}
	<Card title="Accounts" flush>
		{#snippet tools()}
			{#if !off}<NeoButton class="primary" elevation={2} onclick={openAdd}>{#snippet icon()}<PlusIcon size={16} />{/snippet}Account</NeoButton>{/if}
		{/snippet}
		{#if off}
			<Empty icon={UsersIcon} text={sso ? `Managed by ${sso}` : 'Authentication off'} />
		{:else if !users.settled}
			<Skeleton rows={3} dot={false} secondary="10ch" facts={1} action={false} />
		{:else if users.error}
			<Empty text={users.error} action="Retry" onclick={() => users.refresh()} />
		{:else if users.items.length === 0}
			<Empty icon={UsersIcon} text="No accounts" />
		{:else}
			<List head={{ primary: 'Username', facts: ['Role', 'Created'] }}>
				{#each users.items as u (u.id)}
					<ListRow
						primary={u.username}
						facts={[{ value: roleOf(u.role)?.label ?? '', tip: roleOf(u.role)?.grants }, { value: ago(u.createdAt, clockTick.now), tip: when(u.createdAt) }]}
						menu={[
							{ label: 'Set password', onclick: () => openPassword(u) },
							{ label: 'Change role', onclick: () => openRole(u) },
							{ label: 'Delete', danger: true, divider: true, disabled: own(u), description: own(u) ? 'signed in' : undefined, confirm: `Delete ${u.username}?`, onclick: () => remove(u) }
						]}
					/>
				{/each}
			</List>
			<LoadMore next={users.next} loading={users.loading} onmore={() => users.more()} />
		{/if}
	</Card>
{/snippet}

{#if auth}
	<Columns ratio="3/2">
		{#snippet left()}{@render accountsCard()}{/snippet}
		{#snippet right()}
			<Card title="Sessions and proxies">
				{#if form.error && !form.ready}
					<Empty text="Settings failed to load" hint={form.error} action="Retry" onclick={() => form.load()} />
				{:else if !form.ready}
					<Skeleton rows={2} kind="facts" />
				{:else}
					<Duration bind:value={signIn.draft.sessionSeconds} base="s" smallest="min" max={31_536_000} label="Session lifetime" info="How long a browser stays signed in" />
					<Chips bind:value={signIn.draft.trustedProxies} label="Trusted proxies" info="Networks as CIDRs, such as 10.0.0.0/8, whose forwarded address, scheme and host headers are believed" unset="no proxy, forwarded headers are ignored" />
					<SaveBar dirty={signIn.dirty} saving={signIn.saving} error={signIn.error} onsave={() => signIn.save()} ondiscard={() => signIn.discard()} />
				{/if}
			</Card>
		{/snippet}
	</Columns>
{:else}
	{@render accountsCard()}
{/if}

<Dialog bind:open={addOpen} title="New account" confirm="Create" busy={addBusy} needs={!username.trim() ? 'Give the account a username' : !password ? 'Give it a password' : ''} onconfirm={add}>
	<Text bind:value={username} label="Username" max={64} required autocomplete="off" />
	<Secret bind:value={password} label="Password" required autocomplete="new-password" />
	<Segmented bind:value={role} options={roles} label="Role" message={roleOf(role)?.grants} />
	{#snippet footer()}
		{#if addError}<span class="error-text">{addError}</span>{/if}
	{/snippet}
</Dialog>

<Dialog bind:open={roleOpen} title={target ? `Role of ${target.username}` : 'Role'} confirm="Change role" busy={roleBusy} needs={target && nextRole === target.role ? `Already ${roleOf(nextRole)?.label.toLowerCase()}` : ''} onconfirm={setRole}>
	<Segmented bind:value={nextRole} options={roles} label="Role" message={roleOf(nextRole)?.grants} />
	{#if target}<p class="secondary">{own(target) ? 'Changing your own role signs you out' : `Changing the role signs ${target.username} out everywhere`}</p>{/if}
	{#snippet footer()}
		{#if roleError}<span class="error-text">{roleError}</span>{/if}
	{/snippet}
</Dialog>

<Dialog bind:open={passwordOpen} title={target ? `Set a password for ${target.username}` : 'Set a password'} confirm="Set password" busy={passwordBusy} needs={fresh ? '' : 'Enter the new password'} onconfirm={setPassword}>
	<Secret bind:value={fresh} label="New password" required autocomplete="new-password" />
	{#snippet footer()}
		{#if passwordError}<span class="error-text">{passwordError}</span>{/if}
	{/snippet}
</Dialog>
