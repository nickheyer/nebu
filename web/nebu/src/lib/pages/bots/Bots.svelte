<script lang="ts">
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoDialog } from '#lib/neo/floating/dialog/index.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { enumLabel, num } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { BotStateSchema, type Bot, type ProbeBotTokenResponse } from '#lib/proto/nebu/v1/bot_pb.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { botLamp } from '#lib/ui/status.js';
	import { botTip, running } from '#lib/pages/bots/bots.js';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import RobotIcon from 'phosphor-svelte/lib/RobotIcon';

	const list = new ListQuery<Bot, Record<string, never>>(async (_p, _cursor, _limit, signal) => {
		const res = await rpc.bots.listBots({}, { signal });
		return { items: res.bots, next: '' };
	}, [EventKind.BOT]);
	void list.load({});

	// Listed bots with the live stream's copy taking over once it holds them
	const bots = $derived(list.items.map((b) => live.bots.get(b.id) ?? b).sort((a, b) => a.name.localeCompare(b.name)));

	let open = $state(false);
	let name = $state('');
	let token = $state('');
	let enabled = $state(true);
	let probed = $state<ProbeBotTokenResponse | undefined>(undefined);
	let probing = $state(false);
	let creating = $state(false);

	// A probe answers for the token it was given, so a new token clears it
	$effect(() => {
		void token;
		probed = undefined;
	});

	async function probe() {
		probing = true;
		try {
			probed = await rpc.bots.probeBotToken({ token });
		} catch (err) {
			probed = undefined;
			toast.error('Probe failed', reason(err));
		} finally {
			probing = false;
		}
	}

	async function createBot() {
		creating = true;
		try {
			const res = await rpc.bots.createBot({ name: name.trim(), token: token.trim(), enabled });
			toast.success(`Created ${name.trim()}`);
			open = false;
			name = '';
			token = '';
			enabled = true;
			if (res.bot) void goto(`/bots/${res.bot.id}/status`);
		} catch (err) {
			toast.error('Create failed', reason(err));
		} finally {
			creating = false;
		}
	}

	async function start(b: Bot) {
		try {
			await rpc.bots.startBot({ id: b.id });
		} catch (err) {
			toast.error(`Start ${b.name} failed`, reason(err));
		}
	}

	async function stop(b: Bot) {
		try {
			await rpc.bots.stopBot({ id: b.id });
		} catch (err) {
			toast.error(`Stop ${b.name} failed`, reason(err));
		}
	}
</script>

{#snippet avatar(url: string | undefined, size: number)}
	{#if url}
		<img class="avatar" src={url} alt="" width={size} height={size} />
	{:else}
		<span class="avatar muted"><RobotIcon size={size < 32 ? 20 : 32} /></span>
	{/if}
{/snippet}

<Stack gap={16}>
	<Row gap={12}>
		<Cell grow={false}>
			<NeoButton class="primary" elevation={2} onclick={() => (open = true)}>{#snippet icon()}<PlusIcon size={20} />{/snippet}New bot</NeoButton>
		</Cell>
	</Row>
	{#if !list.settled}
		<Skeleton rows={3} />
	{:else if list.error}
		<Empty text={list.error} action="Retry" onclick={() => list.refresh()} />
	{:else if bots.length === 0}
		<Empty text="No bots" action="New bot" onclick={() => (open = true)} />
	{:else}
		<Stack gap={0}>
			{#each bots as b (b.id)}
				{@const on = running(b)}
				<ListRow
					lamp={botLamp(b.state)}
					shape={on ? 'filled' : 'hollow'}
					primary={b.name}
					primaryTip={botTip(b)}
					secondary={enumLabel(BotStateSchema, b.state)}
					secondaryWidth="12ch"
					facts={on ? [{ value: `${num(b.status?.guilds)} guilds` }] : []}
					href="/bots/{b.id}/status"
				>
					{#snippet leading()}{@render avatar(b.status?.avatarUrl, 24)}{/snippet}
					{#snippet action()}
						{#if on}
							<Confirm label="Stop" question="Stop {b.name}" onconfirm={() => stop(b)} />
						{:else}
							<NeoButton text onclick={() => start(b)}>Start</NeoButton>
						{/if}
						<NeoButton text href="/bots/{b.id}/status">Open</NeoButton>
					{/snippet}
				</ListRow>
			{/each}
		</Stack>
	{/if}
</Stack>

<NeoDialog bind:open modal rounded="md" filled elevation={2} width="min(32rem, 92vw)" padding="16px">
	<Stack gap={12}>
		<h3>New bot</h3>
		<Text bind:value={name} label="Name" max={64} required />
		<Row gap={12} align="flex-end" wrap={false}>
			<Cell min="12rem">
				<Secret bind:value={token} label="Token" required />
			</Cell>
			<Cell grow={false}>
				<NeoButton elevation={2} loading={probing} disabled={!token.trim()} onclick={probe}>Probe</NeoButton>
			</Cell>
		</Row>
		{#if probed}
			<Row gap={12} align="flex-start" wrap={false}>
				<Cell grow={false}>{@render avatar(probed.avatarUrl, 40)}</Cell>
				<Cell min="12rem">
					<KeyRows
						dense
						rows={[
							{ label: 'username', value: probed.username },
							{ label: 'shards', value: `${num(probed.recommendedShards)} recommended` },
							{ label: 'invite', value: probed.inviteUrl, href: probed.inviteUrl, copy: true, tip: probed.inviteUrl }
						]}
					/>
				</Cell>
			</Row>
		{/if}
		<Switch bind:checked={enabled} label="Enabled" />
		<Row gap={12} justify="flex-end">
			<NeoButton text onclick={() => (open = false)}>Cancel</NeoButton>
			<NeoButton class="primary" elevation={2} loading={creating} disabled={!name.trim() || !token.trim()} onclick={createBot}>Create</NeoButton>
		</Row>
	</Stack>
</NeoDialog>

<style>
	.avatar {
		display: inline-flex;
		flex: 0 0 auto;
	}
</style>
