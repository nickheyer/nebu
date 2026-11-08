<script lang="ts">
	import { onMount, tick } from 'svelte';
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoDialog } from '#lib/neo/floating/dialog/index.js';
	import { NeoInput } from '#lib/neo/inputs/index.js';
	import { useNeoThemeContext } from '#lib/neo/providers/neo-theme-provider-context.svelte.js';
	import { live } from '#lib/live/events.svelte.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Str from '#lib/ui/Str.svelte';
	import { actionEntries, entries, rank, type Entry } from '#lib/shell/command.js';
	import ArrowElbowDownLeftIcon from 'phosphor-svelte/lib/ArrowElbowDownLeftIcon';
	import MagnifyingGlassIcon from 'phosphor-svelte/lib/MagnifyingGlassIcon';

	const theme = useNeoThemeContext();
	const mac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform);

	let open = $state(false);
	let query = $state('');
	let active = $state(0);
	let input = $state<HTMLInputElement>();
	let list = $state<HTMLElement>();

	const all = $derived(entries(live, session.role, session.reads));
	const actions = $derived(actionEntries(session.reads, query, () => theme.update({ theme: theme.theme === 'dark' ? 'light' : 'dark' })));
	const shown = $derived(rank([...all, ...actions], query));
	// Hits under their group in first hit order, a group named once however its hits interleave in the ranking
	const groups = $derived.by(() => {
		const by = new Map<string, { group: string; items: { entry: Entry; index: number }[] }>();
		shown.forEach((entry, index) => {
			let g = by.get(entry.group);
			if (!g) {
				g = { group: entry.group, items: [] };
				by.set(entry.group, g);
			}
			g.items.push({ entry, index });
		});
		return [...by.values()];
	});

	$effect(() => {
		void shown;
		active = 0;
	});

	export function show() {
		query = '';
		open = true;
		void tick().then(() => input?.focus());
	}

	function close() {
		open = false;
	}

	function choose(e: Entry | undefined) {
		if (!e) return;
		close();
		if (e.run) e.run();
		else if (e.href) void goto(e.href);
	}

	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'ArrowDown') {
			e.preventDefault();
			active = Math.min(shown.length - 1, active + 1);
			scrollTo(active);
		} else if (e.key === 'ArrowUp') {
			e.preventDefault();
			active = Math.max(0, active - 1);
			scrollTo(active);
		} else if (e.key === 'Enter') {
			e.preventDefault();
			choose(shown[active]);
		} else if (e.key === 'Escape') {
			close();
		}
	}

	function scrollTo(i: number) {
		list?.querySelector<HTMLElement>(`[data-index="${i}"]`)?.scrollIntoView({ block: 'nearest' });
	}

	// The shortcut opens the palette from anywhere
	onMount(() => {
		const handler = (e: KeyboardEvent) => {
			if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
				e.preventDefault();
				if (open) close();
				else show();
			}
		};
		window.addEventListener('keydown', handler);
		return () => window.removeEventListener('keydown', handler);
	});
</script>

{#if viewport.mobile}
	<NeoButton text aria-label="Search" onclick={show}>
		{#snippet icon()}<MagnifyingGlassIcon size={20} />{/snippet}
	</NeoButton>
{:else}
	<button type="button" class="trigger" aria-label="Search" onclick={show}>
		<span class="glass"><MagnifyingGlassIcon size={16} /></span>
		<span class="fill"></span>
		<kbd class="key">{mac ? '⌘' : 'Ctrl'}</kbd><kbd class="key">K</kbd>
	</button>
{/if}

<NeoDialog bind:open modal filled rounded="md" elevation={2} placement="top" width="min(40rem, 92vw)" padding="0" blur={1} slide={false} fade={false} closeOnClickOutside onclose={close}>
	<div class="palette" role="combobox" aria-expanded="true" aria-controls="palette-list" aria-haspopup="listbox" tabindex="-1" {onkeydown}>
		<div class="search">
			<NeoInput bind:ref={input} bind:value={query} elevation={0} floating={false} borderless width="100%" aria-label="Search" aria-autocomplete="list" aria-controls="palette-list" autocomplete="off" spellcheck={false}>
				{#snippet before()}<span class="glass"><MagnifyingGlassIcon size={18} /></span>{/snippet}
			</NeoInput>
		</div>
		<div class="results" bind:this={list} id="palette-list" role="listbox" aria-label="Results">
			{#each groups as g (g.group)}
				<div class="group">
					<span class="grouphead">{g.group}</span>
					{#each g.items as { entry, index } (entry.id)}
						<div
							class="entry"
							class:active={index === active}
							role="option"
							aria-selected={index === active}
							data-index={index}
							tabindex="-1"
							onpointerenter={() => (active = index)}
							onclick={() => choose(entry)}
							onkeydown={(e) => {
								if (e.key === 'Enter') choose(entry);
							}}
						>
							<span class="icon"><entry.icon size={18} /></span>
							<span class="label"><Str value={entry.label} kind="text" width="100%" /></span>
							{#if entry.detail}<span class="detail"><Str value={entry.detail} kind="text" color="muted" width="100%" /></span>{/if}
							{#if index === active}<span class="enter"><ArrowElbowDownLeftIcon size={14} /></span>{/if}
						</div>
					{/each}
				</div>
			{:else}
				<div class="nothing">No match</div>
			{/each}
		</div>
	</div>
</NeoDialog>

<style>
	.trigger {
		display: inline-flex;
		gap: 6px;
		align-items: center;
		width: 220px;
		height: 34px;
		padding: 0 8px 0 10px;
		font: inherit;
		color: var(--nebu-text-muted);
		cursor: text;
		background: var(--neo-background-color);
		border: 0;
		border-radius: var(--neo-border-radius);
		box-shadow: var(--neo-box-shadow-inset-1);
		transition:
			box-shadow var(--nebu-fast) var(--nebu-ease),
			color var(--nebu-fast) var(--nebu-ease);
	}
	.trigger:hover,
	.trigger:focus-visible {
		color: var(--neo-text-color);
		box-shadow: var(--neo-box-shadow-inset-2);
	}
	.glass {
		display: inline-flex;
		color: var(--nebu-text-muted);
	}
	.key {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-width: 18px;
		height: 18px;
		padding: 0 5px;
		font-family: var(--nebu-font-mono);
		font-size: 11px;
		line-height: 1;
		color: var(--nebu-text-muted);
		background: var(--nebu-surface-sunken);
		border-radius: 4px;
	}
	.palette {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		min-width: 0;
		max-height: min(70dvh, 560px);
		outline: none;
	}
	.search {
		padding: var(--nebu-space-2) var(--nebu-space-3);
		border-bottom: 1px solid var(--nebu-border);
	}
	.search :global(.neo-input-group) {
		width: 100%;
	}
	.search :global(input) {
		font-size: var(--nebu-text-body);
	}
	.results {
		display: flex;
		flex-direction: column;
		min-height: 0;
		padding: var(--nebu-space-2);
		overflow-y: auto;
	}
	.group {
		display: flex;
		flex-direction: column;
	}
	.group + .group {
		margin-top: var(--nebu-space-2);
	}
	.grouphead {
		padding: var(--nebu-space-1) var(--nebu-space-3);
		font-size: var(--nebu-text-caption);
		color: var(--nebu-text-faint);
		letter-spacing: 0.04em;
	}
	.entry {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		min-height: 38px;
		padding: 0 var(--nebu-space-3);
		cursor: pointer;
		border-radius: var(--neo-border-radius);
		animation: nebu-reveal 220ms var(--nebu-settle) both;
	}
	.entry.active {
		color: var(--neo-color-primary);
		background: color-mix(in srgb, var(--neo-color-primary) 10%, transparent);
	}
	.icon {
		display: inline-flex;
		flex: 0 0 auto;
		color: var(--nebu-text-muted);
	}
	.entry.active .icon {
		color: var(--neo-color-primary);
	}
	.label {
		display: inline-flex;
		flex: 0 1 auto;
		min-width: 0;
	}
	.detail {
		display: inline-flex;
		flex: 1 1 auto;
		min-width: 0;
		font-size: var(--nebu-text-secondary);
	}
	.enter {
		display: inline-flex;
		flex: 0 0 auto;
		color: var(--nebu-text-faint);
	}
	.nothing {
		padding: var(--nebu-space-5);
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		text-align: center;
	}
</style>
