<script lang="ts">
	import { clone, create } from '@bufbuild/protobuf';
	import { untrack } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { capitalize, duration, elapsed, enumLabel, factLabel, shortId, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { SandboxKind, SandboxKindSchema, type Build, type BuildStep, type PreviewBuildResponse, type RecipeStatus, type Var } from '#lib/proto/nebu/v1/recipe_pb.js';
	import { InstallKind, type Install, type InstallOption, type RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { RuntimeDefaultsSchema, SettingsSchema } from '#lib/proto/nebu/v1/settings_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Dot from '#lib/ui/Dot.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows, { type KeyRow } from '#lib/ui/KeyRows.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import KeyValues from '#lib/ui/fields/KeyValues.svelte';
	import Path from '#lib/ui/fields/Path.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Textarea from '#lib/ui/fields/Textarea.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { reveal } from '#lib/ui/motion.svelte.js';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { buildLamp } from '#lib/ui/status.js';
	import ConfigFields from '#lib/pages/runtimes/ConfigFields.svelte';
	import ParamsForm from '#lib/pages/serve/ParamsForm.svelte';
	import { installLabel } from '#lib/pages/serve/serve.js';
	import ArrowDownIcon from 'phosphor-svelte/lib/ArrowDownIcon';
	import ArrowUpIcon from 'phosphor-svelte/lib/ArrowUpIcon';
	import DownloadSimpleIcon from 'phosphor-svelte/lib/DownloadSimpleIcon';
	import HammerIcon from 'phosphor-svelte/lib/HammerIcon';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import SlidersHorizontalIcon from 'phosphor-svelte/lib/SlidersHorizontalIcon';
	import XIcon from 'phosphor-svelte/lib/XIcon';

	let { status, tab = 'installs', ontab, onclose }: { status: RuntimeStatus; tab?: string; ontab: (id: string) => void; onclose: () => void } = $props();

	const rt = $derived(status.runtime);
	const id = $derived(rt?.id ?? '');
	const installList = new ListQuery<Install, { runtimeId: string }>(
		async (p, cursor, limit, signal) => {
			const res = await rpc.runtimes.listInstalls({ runtimeId: p.runtimeId, cursor, limit }, { signal });
			return { items: res.installs, next: res.nextCursor };
		},
		[EventKind.INSTALL]
	);
	const buildList = new ListQuery<Build, { runtimeId: string }>(
		async (p, cursor, limit, signal) => {
			const res = await rpc.build.listBuilds({ runtimeId: p.runtimeId, cursor, limit }, { signal });
			return { items: res.builds, next: res.nextCursor };
		},
		[EventKind.BUILD]
	);
	const installs = $derived(installList.items.map((i) => live.installs.get(i.id) ?? i));
	const builds = $derived(buildList.items.map((b) => live.builds.get(b.id) ?? b));

	// The install form opens over the installs list under its own route
	const installing = $derived(tab === 'install');
	const shown = $derived(installing ? 'installs' : tab);

	$effect(() => {
		if (shown === 'installs') void installList.load({ runtimeId: id });
		else if (shown === 'builds') void buildList.load({ runtimeId: id });
	});
	const tabs = [
		{ id: 'installs', label: 'Installs' },
		{ id: 'builds', label: 'Builds' },
		{ id: 'defaults', label: 'Defaults' }
	];

	let method = $state(untrack(() => status.installs[0]?.method?.id ?? ''));
	const option = $derived(status.installs.find((o) => o.method?.id === method));
	const buildMethod = $derived(status.installs.find((o) => o.method?.kind === InstallKind.BUILT)?.method?.id);
	let adoptPath = $state('');
	let settings = $state<Record<string, string>>({});
	let recipes = $state<RecipeStatus[]>([]);
	let recipesAsked = $state(false);
	let variant = $state('');
	let vars = $state<Record<string, string>>({});
	let sandbox = $state<SandboxKind>(SandboxKind.HOST);
	let image = $state('');
	let ref = $state('');
	let force = $state(false);
	// Recipe patches the build leaves out, every patch applied until switched off
	let skipPatches = $state<string[]>([]);
	// A repository cloned in place of the recipe's source, the recipe's own when empty
	let repo = $state('');
	// Addresses of unified diffs applied after the recipe's patches
	let extraPatches = $state<string[]>([]);
	// Variables every step runs with
	let env = $state<Record<string, string>>({});
	// Runs the steps of the editor in place of the recipe's while switched on
	let ownSteps = $state(false);
	let steps = $state<StepRow[]>([]);
	let previewing = $state(false);
	// What the recipe would run for the form's choices, asked for when its steps are taken into the editor
	let preview = $state<PreviewBuildResponse | undefined>(undefined);
	// Paths copied out of the source tree, the binary installed among them and the longest the build may run, each the recipe's own when empty
	let outputs = $state<string[]>([]);
	let binary = $state('');
	let timeout = $state(0);
	let busy = $state(false);

	const recipe = $derived(option?.recipe ?? recipes.find((r) => r.recipe?.runtimeId === id));
	const variants = $derived((recipe?.recipe?.variants ?? []).map((v) => ({ value: v.id, label: v.id, description: unsupported(v) ? v.requires : v.description, disabled: unsupported(v) })));
	const absent = $derived(recipe?.absentTools ?? []);
	const chosen = $derived(recipe?.recipe?.variants.find((v) => v.id === variant));
	const patches = $derived(recipe?.recipe?.patches ?? []);
	// Tools the chosen variant needs that this host lacks, none inside a container which brings its own
	const missing = $derived(sandbox === SandboxKind.HOST ? [...(recipe?.recipe?.tools ?? []), ...(chosen?.tools ?? [])].filter((t) => absent.includes(t)) : []);

	// A variant with requirements this host lacks
	function unsupported(v: { vars: Record<string, string>; requires: string }): boolean {
		return Object.keys(v.vars).length === 0 && v.requires !== '';
	}

	// Names the lacking tools in a sentence, such as hipcc and cmake
	function missingText(tools: string[]): string {
		const list = tools.length > 1 ? `${tools.slice(0, -1).join(', ')} and ${tools[tools.length - 1]}` : tools[0];
		return `Needs ${list}, which this host lacks`;
	}

	type BuildField = { kind: 'variant' | 'sandbox' | 'image' | 'ref' | 'repo' | 'force' } | { kind: 'var'; v: Var };
	// The fields of a source build two to a row, the ref beside the repository it is of
	const buildFields = $derived<BuildField[]>([
		{ kind: 'variant' },
		{ kind: 'sandbox' },
		{ kind: 'ref' },
		{ kind: 'repo' },
		...(sandbox === SandboxKind.OCI ? [{ kind: 'image' as const }] : []),
		{ kind: 'force' },
		...(recipe?.recipe?.vars ?? []).map((v) => ({ kind: 'var' as const, v }))
	]);

	// Asks for the recipe of a build method the runtime status carries none for
	$effect(() => {
		if (!installing || recipesAsked || option?.method?.kind !== InstallKind.BUILT || option.recipe) return;
		recipesAsked = true;
		void rpc.build
			.listRecipes({ runtimeId: id })
			.then((r) => (recipes = r.recipes))
			.catch((err) => toast.error('Recipes failed to list', reason(err)));
	});

	$effect(() => {
		if (!variant && recipe) {
			variant = recipe.variant || recipe.recipe?.variants[0]?.id || '';
			sandbox = recipe.sandbox || SandboxKind.HOST;
			vars = { ...recipe.vars };
		}
	});

	function methodLabel(o: InstallOption): string {
		const k = o.method?.kind;
		const word = k === InstallKind.ADOPTED ? 'Use existing' : k === InstallKind.BUILT ? 'Build' : 'Download';
		const shared = status.installs.filter((x) => x.method?.kind === k).length > 1;
		return shared ? capitalize(o.method?.id ?? word) : word;
	}
	const methods = $derived(status.installs.map((o) => ({ value: o.method?.id ?? '', label: methodLabel(o), tip: o.method?.description || undefined })));
	const verb = $derived(option?.method?.kind === InstallKind.ADOPTED ? 'Add' : option?.method?.kind === InstallKind.BUILT ? 'Build' : 'Install');

	function pairs<T>(list: T[]): T[][] {
		const out: T[][] = [];
		for (let i = 0; i < list.length; i += 2) out.push(list.slice(i, i + 2));
		return out;
	}

	function togglePatch(id: string, on: boolean) {
		skipPatches = on ? skipPatches.filter((x) => x !== id) : [...skipPatches.filter((x) => x !== id), id];
	}

	// One step of the editor, its command a line sh runs
	type StepRow = { name: string; line: string };

	// The placeholders a step's words take, filled in at build time
	const placeholders = '{{.Src}}, {{.Out}}, {{.Jobs}} and {{.Vars.name}}';

	// A word as sh reads it back, single quoted unless it holds only characters the shell takes as they are
	function shellWord(word: string): string {
		return /^[A-Za-z0-9_@%+=:,./-]+$/.test(word) ? word : `'${word.replaceAll("'", "'\\''")}'`;
	}

	// A step as one shell line, the directory it runs in and the variables it sets ahead of its command
	function stepLine(s: BuildStep): string {
		const ahead = [...(s.dir ? [`cd ${shellWord(s.dir)}`] : []), ...Object.entries(s.env).map(([k, v]) => `export ${k}=${shellWord(v)}`)];
		return [...ahead, s.script.trim() || s.command.map(shellWord).join(' ')].join(' && ');
	}

	// The build the form asks for, everything left empty taken from the recipe
	function request() {
		return {
			runtimeId: id,
			recipeId: recipe?.recipe?.id ?? '',
			variant,
			vars,
			sandbox,
			image,
			force,
			ref,
			repo: repo.trim(),
			skipPatches: skipPatches.filter((x) => patches.some((p) => p.id === x)),
			patches: extraPatches.map((url) => ({ url })),
			env,
			steps: ownSteps ? steps.map((s) => ({ name: s.name.trim(), script: s.line.trim() })) : [],
			outputs,
			binary: binary.trim(),
			timeoutMs: timeout
		};
	}

	// Fills the editor with the steps the recipe runs for the form's choices, or hands the build back to the recipe's own
	async function switchSteps(on: boolean) {
		steps = [];
		if (!on) return;
		previewing = true;
		try {
			// Asked without the outputs, binary and time limit the form sets, so the answer holds the recipe's own
			preview = await rpc.build.previewBuild({ build: { ...request(), steps: [], outputs: [], binary: '', timeoutMs: 0 } });
			steps = preview.steps.map((s) => ({ name: s.name, line: stepLine(s) }));
		} catch (err) {
			ownSteps = false;
			toast.error('Steps failed to load', reason(err));
		} finally {
			previewing = false;
		}
	}

	function addStep() {
		steps = [...steps, { name: '', line: '' }];
	}

	// Moves a step one place earlier or later
	function moveStep(i: number, by: number) {
		const next = [...steps];
		next.splice(i + by, 0, ...next.splice(i, 1));
		steps = next;
	}

	// An existing binary nebu did not find on PATH has to be pointed at
	const needsPath = $derived(option?.method?.kind === InstallKind.ADOPTED && option.unmet.length > 0);
	// What the form still needs before it can run
	const blocked = $derived.by(() => {
		const kind = option?.method?.kind;
		if (kind === InstallKind.ADOPTED && needsPath && adoptPath.trim() === '') return 'Give the path of the binary';
		if (kind === InstallKind.BUILT && !variant) return 'Pick a variant';
		if (kind === InstallKind.BUILT && missing.length > 0) return missingText(missing);
		if (kind === InstallKind.BUILT && previewing) return "Reading the recipe's steps";
		if (kind === InstallKind.BUILT && ownSteps && steps.length === 0) return 'Add a step';
		if (kind === InstallKind.BUILT && ownSteps && steps.some((s) => s.line.trim() === '')) return 'Give every step a command';
		return '';
	});

	function commit() {
		const kind = option?.method?.kind;
		if (kind === InstallKind.ADOPTED) return adopt();
		if (kind === InstallKind.BUILT) return build();
		return install();
	}

	async function adopt() {
		busy = true;
		try {
			await rpc.runtimes.adoptInstall({ runtimeId: id, path: adoptPath });
			toast.success(`Added ${adoptPath || rt?.name}`);
			adoptPath = '';
			ontab('installs');
		} catch (err) {
			toast.error('Add failed', reason(err));
		} finally {
			busy = false;
		}
	}

	async function install() {
		busy = true;
		try {
			const res = await rpc.runtimes.install({ runtimeId: id, method, settings });
			if (!res.task) throw new Error('the daemon answered without a task');
			followTask(res.task, `Install ${rt?.name}`);
			ontab('installs');
		} catch (err) {
			toast.error('Install failed', reason(err));
		} finally {
			busy = false;
		}
	}

	async function build() {
		busy = true;
		try {
			const res = await rpc.build.build(request());
			if (!res.task) throw new Error('the daemon answered without a task');
			followTask(res.task, `Build ${rt?.name}`);
			ontab('installs');
		} catch (err) {
			toast.error('Build failed', reason(err));
		} finally {
			busy = false;
		}
	}

	// Opens the install form on the source build method
	function startBuild() {
		if (buildMethod) method = buildMethod;
		ontab('install');
	}

	async function removeInstall(i: Install) {
		try {
			await rpc.runtimes.removeInstall({ id: i.id });
			toast.success(`Removed ${installLabel(i)}`);
		} catch (err) {
			toast.error('Remove refused', reason(err));
		}
	}

	async function removeBuild(b: Build) {
		try {
			await rpc.build.removeBuild({ id: b.id });
			toast.success(`Removed ${b.variant || b.recipeId}`);
		} catch (err) {
			toast.error('Remove refused', reason(err));
		}
	}

	function users(installId: string) {
		const slots = [...live.slots.values()].filter((s) => s.installId === installId).map((s) => s.name);
		const instances = [...live.instances.values()].filter((i) => i.installId === installId).map((i) => i.name);
		return { slots, instances };
	}

	// How an install came to be, in the words of the method that makes one
	function installWord(i: Install): string {
		return i.kind === InstallKind.ADOPTED ? 'existing' : i.kind === InstallKind.BUILT ? 'built' : 'downloaded';
	}

	type Group = { title: string; rows: KeyRow[] };

	// Facts an install carries about where it came from, shown with the build it was made by
	const madeBy = ['recipe', 'variant', 'commit', 'build'];

	// Names with the count of each said once, in first seen order
	function counted(names: string[]): string[] {
		const by = new Map<string, number>();
		for (const n of names) by.set(n, (by.get(n) ?? 0) + 1);
		return [...by].map(([n, c]) => (c > 1 ? `${n} \u00d7${c}` : n));
	}

	// What an install is, what made it, what it sits beside and what it was probed for, each under its own title
	function installGroups(i: Install): Group[] {
		const facts = Object.entries(i.facts);
		const flag = ([, v]: [string, string]) => v.startsWith('--');
		const file = ([k, v]: [string, string]) => v.startsWith('/') && !madeBy.includes(k);
		const rest = facts.filter((f) => !flag(f) && !file(f) && !madeBy.includes(f[0]) && f[0] !== 'version' && f[0] !== 'devices');
		const build = live.builds.get(i.buildId);
		return [
			{
				title: 'Install',
				rows: [
					{ label: 'version', value: i.version || i.facts.version || '', mono: true },
					{ label: 'added', value: when(i.createdAt) },
					{ label: 'origin', value: i.origin },
					{ label: 'binary', value: i.path, mono: true, copy: true },
					{ label: 'directory', value: i.dir, mono: true, copy: true },
					{ label: 'devices', value: (i.facts.devices ?? '').split(',').join(', '), mono: true }
				]
			},
			{
				title: 'Build',
				rows: [
					{ label: 'recipe', value: [i.facts.recipe, i.facts.variant].filter(Boolean).join(', ') },
					{ label: 'ref', value: build?.ref ?? '', mono: true },
					{ label: 'commit', value: i.facts.commit ?? '', mono: true, copy: true },
					{ label: 'build', value: i.facts.build ?? '', mono: true, href: i.facts.build ? `/runtimes/${encodeURIComponent(id)}/builds` : undefined }
				]
			},
			{ title: 'Beside the binary', rows: facts.filter(file).map(([k, v]) => ({ label: factLabel(k), value: v, mono: true, copy: true })) },
			{ title: 'Detected', rows: rest.map(([k, v]) => ({ label: factLabel(k), value: v, mono: true })) }
		].filter((g) => g.rows.some((r) => r.value !== ''));
	}

	// The command line flags a probe found the binary to take, in order
	function installFlags(i: Install): string[] {
		return Object.values(i.facts)
			.filter((v) => v.startsWith('--'))
			.sort();
	}

	// What a build was made from, where it ran, the options it was given, what it set in place of the recipe's and the host it was built for
	function buildGroups(b: Build): Group[] {
		const own = b.request;
		return [
			{
				title: 'Build',
				rows: [
					{ label: 'ref', value: b.ref, mono: true },
					{ label: 'commit', value: b.commit, mono: true, copy: true },
					{ label: 'built in', value: [enumLabel(SandboxKindSchema, b.sandbox), b.image].filter(Boolean).join(', ') },
					{ label: 'started', value: when(b.createdAt) },
					{ label: 'took', value: duration(b.createdAt, b.finishedAt, clockTick.now) },
					{ label: 'task', value: shortId(b.taskId), href: b.taskId ? `/tasks/${b.taskId}` : undefined, mono: true },
					{ label: 'binary', value: b.binary, mono: true, copy: true },
					{ label: 'directory', value: b.dir, mono: true, copy: true }
				]
			},
			{
				title: "In place of the recipe's",
				rows: [
					{ label: 'repository', value: own?.repo ?? '', mono: true, copy: true },
					{ label: 'outputs', value: (own?.outputs ?? []).join(', '), mono: true },
					{ label: 'binary', value: own?.binary ?? '', mono: true },
					{ label: 'timeout', value: own?.timeoutMs ? elapsed(own.timeoutMs) : '' }
				]
			},
			{ title: 'Options', rows: Object.entries(b.vars).map(([k, v]) => ({ label: factLabel(k), value: v, mono: true })) },
			{ title: 'Environment', rows: Object.entries(own?.env ?? {}).map(([k, v]) => ({ label: k, value: v, mono: true })) },
			{ title: 'Steps', rows: (own?.steps ?? []).map((s, n) => ({ label: s.name || `step ${n + 1}`, value: stepLine(s), mono: true })) },
			{ title: 'Built for', rows: Object.entries(b.facts).map(([k, v]) => ({ label: factLabel(k), value: v.split(',').join(', '), mono: true })) },
			{ title: 'Patches', rows: b.patches.map((p, n) => ({ label: String(n + 1), value: p, mono: true })) },
			{ title: 'Extra patches', rows: (own?.patches ?? []).map((p, n) => ({ label: String(n + 1), value: p.url || p.description || p.id, mono: true })) }
		].filter((g) => g.rows.some((r) => r.value !== ''));
	}

	// The stored defaults for this runtime, merged under every slot and launch
	const stored = $derived(live.settings?.runtimeDefaults[id]?.params ?? {});
	let draft = $state<Record<string, string>>({});
	let draftFrom = '';
	let savingDefaults = $state(false);
	let defaultsError = $state<string | undefined>(undefined);
	const draftDirty = $derived(JSON.stringify(draft) !== JSON.stringify(stored));

	// Takes the stored defaults into the form whenever they change underneath it
	$effect(() => {
		const key = JSON.stringify(stored);
		if (key === draftFrom) return;
		draftFrom = key;
		draft = { ...stored };
	});

	async function saveDefaults() {
		savingDefaults = true;
		defaultsError = undefined;
		try {
			const base = live.settings ?? (await rpc.settings.getSettings({})).settings;
			if (!base) throw new Error('the daemon answered without settings');
			const settings = clone(SettingsSchema, base);
			const params = Object.fromEntries(Object.entries(draft).filter(([, v]) => v.trim() !== ''));
			if (Object.keys(params).length) settings.runtimeDefaults[id] = create(RuntimeDefaultsSchema, { params });
			else delete settings.runtimeDefaults[id];
			await rpc.settings.updateSettings({ settings });
			toast.success(`Saved ${rt?.name ?? id} defaults`);
		} catch (err) {
			defaultsError = reason(err);
		} finally {
			savingDefaults = false;
		}
	}
</script>

{#snippet buildField(f: BuildField)}
	{#if f.kind === 'variant'}
		<Select bind:value={variant} options={variants} label="Variant" />
	{:else if f.kind === 'sandbox'}
		<Segmented bind:value={sandbox} options={[{ value: SandboxKind.HOST, label: 'Host' }, { value: SandboxKind.OCI, label: 'Container' }]} label="Build in" />
	{:else if f.kind === 'image'}
		<Text bind:value={image} label="Container image" mono />
	{:else if f.kind === 'ref'}
		<Text bind:value={ref} label="Branch, tag or commit" max={64} mono />
	{:else if f.kind === 'repo'}
		<Text bind:value={repo} label="Repository" placeholder="owner/name or a git URL" unset={recipe?.recipe?.source || undefined} max={256} mono />
	{:else if f.kind === 'force'}
		<Switch bind:checked={force} label="Rebuild when already built" />
	{:else if f.kind === 'var'}
		{@const v = f.v}
		{#if v.choices.length}
			<Select value={vars[v.name] || v.default} options={v.choices.map((c) => ({ value: c, label: c }))} label={v.label || v.name} info={v.description || undefined} onchange={(x) => (vars[v.name] = x ?? '')} />
		{:else}
			<Text value={vars[v.name] ?? ''} label={v.label || v.name} info={v.description || undefined} unset={v.default || undefined} onchange={(x) => (vars[v.name] = x)} />
		{/if}
	{/if}
{/snippet}

{#snippet stepEditor()}
	<Stack gap={8}>
		{#each steps as s, i (s)}
			<Row gap={8} wrap={false} align="flex-start">
				<Cell min="7rem" max="11rem"><Text bind:value={s.name} placeholder="Name" aria="Step name" max={64} /></Cell>
				<Cell min="12rem"><Textarea bind:value={s.line} placeholder="Command" rows={1} maxRows={8} mono /></Cell>
				<Cell grow={false}>
					<span class="trail">
						<NeoButton text aria-label="Move step up" title="Move up" disabled={i === 0} onclick={() => moveStep(i, -1)}>
							{#snippet icon()}<ArrowUpIcon size={16} />{/snippet}
						</NeoButton>
						<NeoButton text aria-label="Move step down" title="Move down" disabled={i === steps.length - 1} onclick={() => moveStep(i, 1)}>
							{#snippet icon()}<ArrowDownIcon size={16} />{/snippet}
						</NeoButton>
						<NeoButton text aria-label="Remove step" title="Remove" onclick={() => (steps = steps.filter((_, j) => j !== i))}>
							{#snippet icon()}<XIcon size={16} />{/snippet}
						</NeoButton>
					</span>
				</Cell>
			</Row>
		{/each}
		<Row>
			<NeoButton text onclick={addStep}>
				{#snippet icon()}<PlusIcon size={16} />{/snippet}
				Step
			</NeoButton>
		</Row>
		<p class="secondary">{placeholders} are filled in at build time</p>
	</Stack>
{/snippet}

{#snippet groups(list: Group[])}
	{#each list as g (g.title)}
		<section class="group" aria-label={g.title}>
			<h3 class="label">{g.title}</h3>
			<KeyRows dense rows={g.rows} />
		</section>
	{/each}
{/snippet}

{#snippet installForm()}
	<div class="form" in:reveal>
		<Stack gap={24}>
			<FormSection>
				<Select bind:value={method} options={methods} label="Method" />
				{#if option?.unmet.length}
					<div class="unmet secondary"><Dot lamp="warning" label="unmet" /><Str value={option.unmet.join(', ')} kind="message" color="muted" width="100%" /></div>
				{/if}
			</FormSection>
			{#if option?.method?.kind === InstallKind.ADOPTED}
				<FormSection>
					<Path bind:value={adoptPath} label="Binary" filter="executables" required={needsPath} unset={needsPath ? undefined : 'the one found on PATH'} />
				</FormSection>
			{:else if option?.method?.kind === InstallKind.PREBUILT}
				{#if option.fields.length}
					<FormSection>
						<ConfigFields fields={option.fields} bind:values={settings} />
					</FormSection>
				{/if}
			{:else if option?.method?.kind === InstallKind.BUILT}
				<FormSection>
					{#each pairs(buildFields) as pair, n (n)}
						<FieldRow>
							{#each pair as f (f.kind === 'var' ? `var:${f.v.name}` : f.kind)}{@render buildField(f)}{/each}
						</FieldRow>
					{/each}
					{#snippet advanced()}
						<Chips bind:value={extraPatches} label="Extra patches" info="Patches fetched from these addresses and applied after the recipe's own" placeholder="https://" />
						<KeyValues bind:value={env} label="Environment" info="Variables every step runs with" keyLabel="Name" addLabel="Variable" />
						<Switch bind:checked={ownSteps} label="Replace the recipe's steps" info="Starts from the steps the recipe runs for these choices" disabled={previewing} onchange={switchSteps} />
						{#if previewing}
							<Skeleton rows={2} kind="bars" />
						{:else if ownSteps}
							{@render stepEditor()}
						{/if}
						<Chips bind:value={outputs} label="Outputs" info="Paths in the source tree copied out after the steps, such as build/bin/*" unset={preview?.outputs.join(', ') || "the recipe's own"} />
						<FieldRow wide>
							<Text bind:value={binary} label="Binary" info="The file the install runs, relative to what was copied out" unset={preview?.binary || "the recipe's own"} mono />
							<Duration bind:value={timeout} smallest="min" max={86_400_000} label="Timeout" emptyZero fallback={recipe?.recipe?.timeoutMs || undefined} unset="the recipe's limit" />
						</FieldRow>
					{/snippet}
				</FormSection>
				{#if patches.length}
					<FormSection title="Patches">
						<List head={{ primary: 'Patch', action: 'Apply' }} label="Patches" compactBelow={0}>
							{#each patches as p (p.id)}
								<ListRow primary={p.id} primaryKind="version">
									{#snippet action()}
										<Switch checked={!skipPatches.includes(p.id)} aria={`Apply ${p.id}`} onchange={(on) => togglePatch(p.id, on)} />
									{/snippet}
									{#snippet below()}<span class="secondary muted">{p.description}</span>{/snippet}
								</ListRow>
							{/each}
						</List>
					</FormSection>
				{/if}
			{/if}
		</Stack>
		<SaveBar always dirty saving={busy} label={verb} needs={blocked} onsave={commit} ondiscard={() => ontab('installs')} />
	</div>
{/snippet}

<PanelHeader title={rt?.name ?? id} subtitle={rt?.description || undefined} {onclose} />
<div class="tabrow">
	<PageTabs {tabs} active={shown} onchange={ontab} />
	{#if shown === 'installs' && status.installs.length && !installing}
		<NeoButton elevation={2} onclick={() => ontab('install')}>
			{#snippet icon()}<DownloadSimpleIcon size={16} />{/snippet}
			Install
		</NeoButton>
	{:else if shown === 'builds' && buildMethod}
		<NeoButton elevation={2} onclick={startBuild}>
			{#snippet icon()}<HammerIcon size={16} />{/snippet}
			Build
		</NeoButton>
	{/if}
</div>
<div class="body">
	{#if shown === 'installs'}
		{#if installing}
			{@render installForm()}
		{:else if !installList.settled}
			<Skeleton rows={2} primary="12ch" secondary="24ch" facts={0} menu={false} />
		{:else if installList.error}
			<Empty text="Installs failed to list" hint={installList.error} action="Retry" onclick={() => installList.refresh()} />
		{:else if installs.length === 0}
			{#if !installing}
				{#if status.installs.length}
					<Empty icon={DownloadSimpleIcon} text="Not installed" action="Install" primary onclick={() => ontab('install')} />
				{:else}
					<Empty icon={DownloadSimpleIcon} text="No install method" />
				{/if}
			{/if}
		{:else}
			<List head={{ primary: 'Version', secondary: 'Method' }}>
				{#each installs as i (i.id)}
					{@const using = users(i.id)}
					{@const used = using.slots.length + using.instances.length > 0}
					<ListRow lamp={used ? 'success' : 'muted'} shape={used ? 'filled' : 'hollow'} primary={installLabel(i)} primaryKind="version" primaryWidth="12ch" secondary={installWord(i)} secondaryWidth="12ch">
						{#snippet action()}
							<Confirm label="Remove" question="Remove {installLabel(i)}?" danger onconfirm={() => removeInstall(i)} />
						{/snippet}
						{#snippet expand()}
							{@const flags = installFlags(i)}
							{@render groups(installGroups(i))}
							{#if flags.length}
								<section class="group" aria-label="Flags it takes">
									<h3 class="label">Flags it takes</h3>
									<div class="flags">{#each flags as f (f)}<span class="flag">{f}</span>{/each}</div>
								</section>
							{/if}
							{#if i.status?.conditions.length}
								<section class="group" aria-label="Checks">
									<h3 class="label">Checks</h3>
									<Conditions conditions={i.status.conditions} />
								</section>
							{/if}
							{#if used}
								{@render groups([{ title: 'Used by', rows: [{ label: 'slots', value: counted(using.slots).join(', ') }, { label: 'instances', value: counted(using.instances).join(', ') }] }])}
							{/if}
						{/snippet}
					</ListRow>
				{/each}
			</List>
			<LoadMore next={installList.next} loading={installList.loading} onmore={() => installList.more()} />
		{/if}
	{:else if shown === 'builds'}
		{#if !buildList.settled}
			<Skeleton rows={2} primary="16ch" secondary="12ch" facts={1} />
		{:else if buildList.error}
			<Empty text="Builds failed to list" hint={buildList.error} action="Retry" onclick={() => buildList.refresh()} />
		{:else if builds.length === 0}
			{#if buildMethod}
				<Empty icon={HammerIcon} text="No builds" hint="A build compiles the runtime from source for this host" />
			{:else}
				<Empty icon={HammerIcon} text="No builds" />
			{/if}
		{:else}
			<List head={{ primary: 'Variant', secondary: 'Ref', facts: ['Duration'] }}>
				{#each builds as b (b.id)}
					<ListRow lamp={buildLamp(b.state)} primary={b.variant || b.recipeId} secondary={b.ref} secondaryKind="version" secondaryWidth="12ch" facts={[{ value: duration(b.createdAt, b.finishedAt, clockTick.now) }]}>
						{#snippet action()}
							<Confirm label="Remove" question="Remove build {b.variant || b.recipeId}?" danger onconfirm={() => removeBuild(b)} />
						{/snippet}
						{#snippet expand()}
							{#if b.error}<p class="error-text failure">{b.error}</p>{/if}
							{@render groups(buildGroups(b))}
						{/snippet}
					</ListRow>
				{/each}
			</List>
			<LoadMore next={buildList.next} loading={buildList.loading} onmore={() => buildList.more()} />
		{/if}
	{:else if shown === 'defaults'}
		{#if !rt?.params.length}
			<Empty icon={SlidersHorizontalIcon} text="No parameters" />
		{:else}
			<Stack gap={24}>
				<ParamsForm params={rt.params} bind:values={draft} restingWord="Built in" />
			</Stack>
			<SaveBar dirty={draftDirty} saving={savingDefaults} error={defaultsError} onsave={saveDefaults} ondiscard={() => (draft = { ...stored })} />
		{/if}
	{/if}
</div>

<style>
	/* Each kind of fact sits under its own title */
	.group {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-1);
		min-width: 0;
	}
	.group + .group {
		margin-top: var(--nebu-space-2);
	}
	.flags {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 6px;
	}
	.flag {
		padding: 1px 6px;
		font-family: var(--nebu-font-mono);
		font-size: var(--nebu-text-caption);
		background: var(--nebu-surface-sunken);
		border-radius: 4px;
		box-shadow: var(--neo-box-shadow-inset-1);
	}
	.tabrow {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		justify-content: space-between;
		min-width: 0;
	}
	.body {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-4);
		min-width: 0;
		margin-top: var(--nebu-space-3);
	}
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-4);
		padding-bottom: var(--nebu-space-4);
		border-bottom: 1px solid var(--nebu-border);
	}
	.form:last-child {
		padding-bottom: 0;
		border-bottom: 0;
	}
	.failure {
		overflow-wrap: anywhere;
		white-space: pre-wrap;
	}
	.unmet {
		display: flex;
		gap: var(--nebu-space-2);
		align-items: center;
		min-width: 0;
	}
</style>
