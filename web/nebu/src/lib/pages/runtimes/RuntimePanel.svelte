<script lang="ts">
	import { untrack } from 'svelte';
	import { NeoProgressBar } from '#lib/neo/progress/index.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { isCancel, reason, rpc } from '#lib/api/transport.js';
	import { capitalize, duration, enumLabel, percent, shortId, thin, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { SandboxKind, SandboxKindSchema, type Build, type RecipeStatus, type Var } from '#lib/proto/nebu/v1/recipe_pb.js';
	import { InstallKind, InstallKindSchema, ParamTypeSchema, type Install, type InstallOption, type Param, type RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { TaskState, TaskStateSchema, type Task } from '#lib/proto/nebu/v1/task_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Dot from '#lib/ui/Dot.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import LogView from '#lib/ui/LogView.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import ShowAll from '#lib/ui/ShowAll.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Str from '#lib/ui/Str.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import Path from '#lib/ui/fields/Path.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { buildLamp, taskLamp } from '#lib/ui/status.js';
	import ConfigFields from '#lib/pages/runtimes/ConfigFields.svelte';
	import DownloadSimpleIcon from 'phosphor-svelte/lib/DownloadSimpleIcon';
	import HammerIcon from 'phosphor-svelte/lib/HammerIcon';
	import SlidersHorizontalIcon from 'phosphor-svelte/lib/SlidersHorizontalIcon';

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
	// The listed rows with the copies the live stream updated since
	const installs = $derived(installList.items.map((i) => live.installs.get(i.id) ?? i));
	const builds = $derived(buildList.items.map((b) => live.builds.get(b.id) ?? b));

	$effect(() => {
		if (tab === 'installs') void installList.load({ runtimeId: id });
		else if (tab === 'builds') void buildList.load({ runtimeId: id });
	});
	const tabs = [
		{ id: 'installs', label: 'Installs' },
		{ id: 'install', label: 'Install' },
		{ id: 'builds', label: 'Builds' },
		{ id: 'params', label: 'Params' }
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
	let busy = $state(false);
	let followed = $state<Task | undefined>(undefined);
	let advancedOpen = $state<Record<string, boolean>>({});
	const followedTask = $derived(followed ? (live.tasks.get(followed.id) ?? followed) : undefined);

	const recipe = $derived(option?.recipe ?? recipes.find((r) => r.recipe?.runtimeId === id));
	const variants = $derived((recipe?.recipe?.variants ?? []).map((v) => ({ value: v.id, label: v.id, description: unsupported(v) ? v.requires : v.description, disabled: unsupported(v) })));
	const absent = $derived(recipe?.absentTools ?? []);

	// A variant this host cannot build, one with requirements and no settings for the host
	function unsupported(v: { vars: Record<string, string>; requires: string }): boolean {
		return Object.keys(v.vars).length === 0 && v.requires !== '';
	}

	type BuildField = { kind: 'variant' | 'sandbox' | 'image' | 'ref' | 'force' } | { kind: 'var'; v: Var };
	const buildFields = $derived<BuildField[]>([
		{ kind: 'variant' },
		{ kind: 'sandbox' },
		...(sandbox === SandboxKind.OCI ? [{ kind: 'image' as const }] : []),
		{ kind: 'ref' },
		{ kind: 'force' },
		...(recipe?.recipe?.vars ?? []).map((v) => ({ kind: 'var' as const, v }))
	]);

	// Asks for the recipe of a build method the runtime status carries none for
	$effect(() => {
		if (tab !== 'install' || recipesAsked || option?.method?.kind !== InstallKind.BUILT || option.recipe) return;
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

	// The method's kind as its label, its id when several methods share a kind
	function methodLabel(o: InstallOption): string {
		const k = o.method?.kind;
		const word = k === InstallKind.ADOPTED ? 'Adopt' : k === InstallKind.BUILT ? 'Build' : 'Prebuilt';
		const shared = status.installs.filter((x) => x.method?.kind === k).length > 1;
		return shared ? capitalize(o.method?.id ?? word) : word;
	}
	const methods = $derived(status.installs.map((o) => ({ value: o.method?.id ?? '', label: methodLabel(o), tip: o.method?.description || undefined })));
	// The verb the install form commits with, by the chosen method's kind
	const verb = $derived(option?.method?.kind === InstallKind.ADOPTED ? 'Adopt' : option?.method?.kind === InstallKind.BUILT ? 'Build' : 'Install');

	// The ways in, as one phrase naming the methods the runtime offers
	const ways = $derived.by(() => {
		const kinds = new Set(status.installs.map((o) => o.method?.kind));
		const out: string[] = [];
		if (kinds.has(InstallKind.PREBUILT)) out.push('install a prebuilt release');
		if (kinds.has(InstallKind.BUILT)) out.push('build from source');
		if (kinds.has(InstallKind.ADOPTED)) out.push('adopt a binary already on this host');
		return out.length > 1 ? `${capitalize(out.slice(0, -1).join(', '))} or ${out.at(-1)}` : capitalize(out[0] ?? '');
	});

	function pairs<T>(list: T[]): T[][] {
		const out: T[][] = [];
		for (let i = 0; i < list.length; i += 2) out.push(list.slice(i, i + 2));
		return out;
	}

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
			toast.success(`Adopted ${adoptPath || rt?.name}`);
			adoptPath = '';
			ontab('installs');
		} catch (err) {
			toast.error('Adopt failed', reason(err));
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
			followed = res.task;
		} catch (err) {
			toast.error('Install failed', reason(err));
		} finally {
			busy = false;
		}
	}

	async function build() {
		busy = true;
		try {
			const res = await rpc.build.build({ runtimeId: id, recipeId: recipe?.recipe?.id ?? '', variant, vars, sandbox, image, force, ref });
			if (!res.task) throw new Error('the daemon answered without a task');
			followTask(res.task, `Build ${rt?.name}`);
			followed = res.task;
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
			toast.success(`Removed ${i.version || i.path}`);
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
		const slots = [...live.slots.values()].filter((s) => s.request?.installId === installId).map((s) => s.name);
		const instances = [...live.instances.values()].filter((i) => i.installId === installId).map((i) => i.name);
		return { slots, instances };
	}

	function installTip(i: Install): string {
		return [`path ${i.path}`, i.dir ? `directory ${i.dir}` : '', `created ${when(i.createdAt)}`].filter(Boolean).join('\n');
	}

	function buildTip(b: Build): string {
		return [b.commit ? `commit ${b.commit}` : '', `sandbox ${enumLabel(SandboxKindSchema, b.sandbox)}`, b.image ? `image ${b.image}` : '', b.dir ? `directory ${b.dir}` : '', b.binary ? `binary ${b.binary}` : ''].filter(Boolean).join('\n');
	}

	function paramTip(p: Param): string {
		return [p.name, p.description, p.flag ? `flag ${p.flag}` : '', p.env ? `env ${p.env}` : '', p.choices.length ? `choices ${p.choices.join(', ')}` : '', p.step ? `step ${p.step}` : ''].filter(Boolean).join('\n');
	}

	function boundsOf(p: Param): string {
		return p.max > p.min ? `${p.min} to ${p.max}${p.unit ? thin + p.unit : ''}` : '';
	}

	// The default with its unit, such as 4096 tokens
	function defaultOf(p: Param): string {
		if (p.default === '') return 'none';
		return p.unit ? `${p.default}${thin}${p.unit}` : p.default;
	}

	function ruleOf(p: Param): string {
		return p.solved ? p.rule || 'chosen by the planner at launch' : '';
	}

	function taskActive(t: Task): boolean {
		return t.state === TaskState.PENDING || t.state === TaskState.RUNNING;
	}

	// A log source following the task while it runs and reading its stored tail once it ended
	function taskLogs(taskId: string, running: boolean) {
		return (tail: number, follow: boolean, signal: AbortSignal): AsyncIterable<string[]> =>
			(async function* () {
				if (running && follow) {
					try {
						for await (const res of rpc.tasks.watchTask({ id: taskId }, { signal })) if (res.logs.length) yield res.logs;
					} catch (err) {
						if (!isCancel(err)) throw err;
					}
					return;
				}
				const res = await rpc.tasks.getTask({ id: taskId }, { signal });
				yield res.logs.slice(-tail);
			})();
	}

	// Params grouped in first seen order with the advanced ones apart, as the launch form renders them
	const groups = $derived.by(() => {
		const order: string[] = [];
		const by = new Map<string, { plain: Param[]; advanced: Param[] }>();
		for (const p of rt?.params ?? []) {
			const g = p.group || 'Parameters';
			if (!by.has(g)) {
				by.set(g, { plain: [], advanced: [] });
				order.push(g);
			}
			(p.advanced ? by.get(g)!.advanced : by.get(g)!.plain).push(p);
		}
		return order.map((g) => ({ title: g, ...by.get(g)! }));
	});
</script>

{#snippet buildField(f: BuildField)}
	{#if f.kind === 'variant'}
		<Select bind:value={variant} options={variants} label="Variant" />
	{:else if f.kind === 'sandbox'}
		<Segmented bind:value={sandbox} options={[{ value: SandboxKind.HOST, label: 'Host' }, { value: SandboxKind.OCI, label: 'OCI' }]} label="Sandbox" />
	{:else if f.kind === 'image'}
		<Text bind:value={image} label="Image" placeholder={recipe?.sandboxCli || undefined} mono />
	{:else if f.kind === 'ref'}
		<Text bind:value={ref} label="Ref" max={64} mono placeholder="default branch" />
	{:else if f.kind === 'force'}
		<Switch bind:checked={force} label="Force" info="Builds again when a build of this ref exists" />
	{:else if f.kind === 'var'}
		{@const v = f.v}
		{#if v.choices.length}
			<Select value={vars[v.name] || v.default} options={v.choices.map((c) => ({ value: c, label: c }))} label={v.label || v.name} info={v.description || undefined} onchange={(x) => (vars[v.name] = x ?? '')} />
		{:else}
			<Text value={vars[v.name] ?? ''} label={v.label || v.name} info={v.description || undefined} placeholder={v.default} onchange={(x) => (vars[v.name] = x)} />
		{/if}
	{/if}
{/snippet}

{#snippet paramRow(p: Param)}
	<ListRow primary={p.label || p.name} primaryTip={paramTip(p)} secondary={defaultOf(p)} secondaryKind="text" secondaryWidth="16ch" facts={[{ value: boundsOf(p) }, { value: p.solved ? 'planner' : '', tip: ruleOf(p) }]}>
		{#snippet badges()}<StatePill label={enumLabel(ParamTypeSchema, p.type)} />{/snippet}
		{#snippet expand()}
			<KeyRows
				dense
				rows={[
					{ label: 'type', value: enumLabel(ParamTypeSchema, p.type) },
					{ label: 'default', value: p.default, mono: true },
					{ label: 'unit', value: p.unit },
					{ label: 'bounds', value: boundsOf(p) },
					{ label: 'auto rule', value: ruleOf(p), tip: ruleOf(p) },
					{ label: 'choices', value: p.choices.join(', '), tip: p.choices.join('\n') },
					{ label: 'flag', value: p.flag, mono: true },
					{ label: 'env', value: p.env, mono: true }
				]}
			/>
		{/snippet}
	</ListRow>
{/snippet}

{#snippet followedBlock(t: Task)}
	{@const running = taskActive(t)}
	{@const done = percent(t.progress?.done, t.progress?.total)}
	<Section title="Task" level={3}>
		{#snippet tools()}
			<StatePill label={enumLabel(TaskStateSchema, t.state)} lamp={taskLamp(t.state)} />
			<RowAction href="/tasks/{t.id}" label="Open" />
		{/snippet}
		<Stack gap={12}>
			<NeoProgressBar value={running ? (done ?? 0) : 100} indeterminate={running && done === undefined} elevation={-1} height="6px" width="100%" color={t.state === TaskState.FAILED ? 'error' : 'primary'} immediate />
			{#if t.progress?.message}<Str value={t.progress.message} kind="message" color="muted" width="100%" />{/if}
			{#if t.error}<p class="error-text failure">{t.error}</p>{/if}
			{#key `${t.id}:${running}`}
				<LogView source={taskLogs(t.id, running)} name={t.title} height="30vh" />
			{/key}
		</Stack>
	</Section>
{/snippet}

<PanelHeader title={rt?.name ?? id} subtitle={rt?.description || undefined} {onclose} />
<PageTabs {tabs} active={tab} onchange={ontab} />
<div class="body">
	{#if tab === 'installs'}
		{#if !installList.settled}
			<Skeleton rows={2} primary="12ch" secondary="24ch" facts={0} menu={false} />
		{:else if installList.error}
			<Empty text="Installs failed to list" hint={installList.error} action="Retry" onclick={() => installList.refresh()} />
		{:else if installs.length === 0}
			{#if status.installs.length}
				<Empty icon={DownloadSimpleIcon} text="Not installed" hint="{ways}, then launch models on it from Serve" action="Install" primary onclick={() => ontab('install')} />
			{:else}
				<Empty icon={DownloadSimpleIcon} text="Not installed" hint="This runtime offers no way to install it on this host" />
			{/if}
		{:else}
			<List head={{ primary: 'Version', secondary: 'Origin' }}>
				{#each installs as i (i.id)}
					{@const using = users(i.id)}
					{@const used = using.slots.length + using.instances.length > 0}
					<ListRow lamp={used ? 'success' : 'muted'} shape={used ? 'filled' : 'hollow'} primary={i.version || 'unversioned'} primaryKind="version" primaryWidth="12ch" primaryTip={installTip(i)} secondary={i.origin || i.path} secondaryKind="text" secondaryWidth="32ch">
						{#snippet badges()}<StatePill label={enumLabel(InstallKindSchema, i.kind)} />{/snippet}
						{#snippet action()}
							<Confirm label="Remove" question="Remove {i.version || i.path}?" danger onconfirm={() => removeInstall(i)} />
						{/snippet}
						{#snippet expand()}
							<KeyRows dense rows={Object.entries(i.facts).map(([k, v]) => ({ label: k, value: v, mono: true, tip: v }))} />
							<Conditions conditions={i.status?.conditions} />
							<KeyRows
								dense
								rows={used
									? [
											{ label: 'slots', value: using.slots.join(', '), tip: using.slots.join('\n') },
											{ label: 'instances', value: using.instances.join(', '), tip: using.instances.join('\n') }
										]
									: [{ label: 'used by', value: 'nothing' }]}
							/>
						{/snippet}
					</ListRow>
				{/each}
			</List>
			<LoadMore next={installList.next} loading={installList.loading} onmore={() => installList.more()} />
		{/if}
	{:else if tab === 'install'}
		{#if status.installs.length === 0}
			<Empty icon={DownloadSimpleIcon} text="No install method" hint="This runtime offers no way to install it on this host" />
		{:else}
			<Stack gap={24}>
				<FormSection>
					{#if methods.length > 4}
						<Select bind:value={method} options={methods} label="Method" />
					{:else}
						<Segmented bind:value={method} options={methods} label="Method" />
					{/if}
					{#if option?.unmet.length}
						<div class="unmet secondary"><Dot lamp="warning" label="unmet" /><Str value={option.unmet.join(', ')} kind="message" color="muted" tip={option.unmet.join('\n')} width="100%" /></div>
					{/if}
				</FormSection>
				{#if option?.method?.kind === InstallKind.ADOPTED}
					<FormSection>
						<Path bind:value={adoptPath} label="Binary" info="A binary already on this host, empty searches PATH" placeholder={option.method.binaries.join(', ')} filter="executables" />
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
						{#if absent.length}
							<Str value="Missing tools {absent.join(', ')}" kind="message" color="error" tip={absent.join('\n')} width="100%" />
						{/if}
					</FormSection>
				{/if}
				{#if followedTask}{@render followedBlock(followedTask)}{/if}
			</Stack>
			<SaveBar
				always
				dirty
				saving={busy}
				label={verb}
				disabled={option?.method?.kind === InstallKind.BUILT && (absent.length > 0 || !variant)}
				onsave={commit}
				ondiscard={() => ontab('installs')}
			/>
		{/if}
	{:else if tab === 'builds'}
		{#if !buildList.settled}
			<Skeleton rows={2} primary="16ch" secondary="12ch" facts={1} />
		{:else if buildList.error}
			<Empty text="Builds failed to list" hint={buildList.error} action="Retry" onclick={() => buildList.refresh()} />
		{:else if builds.length === 0}
			{#if buildMethod}
				<Empty icon={HammerIcon} text="No builds" hint="Builds from source land here with their variant, ref and log" action="Build" onclick={startBuild} />
			{:else}
				<Empty icon={HammerIcon} text="No builds" hint="This runtime installs from releases or binaries alone, never from source" />
			{/if}
		{:else}
			<List head={{ primary: 'Variant', secondary: 'Ref', facts: ['Took'] }}>
				{#each builds as b (b.id)}
					<ListRow lamp={buildLamp(b.state)} primary={b.variant || b.recipeId} primaryTip={buildTip(b)} secondary={b.ref || 'default'} secondaryKind="version" secondaryWidth="12ch" facts={[{ value: duration(b.createdAt, b.finishedAt, clockTick.now) }]}>
						{#snippet action()}
							<Confirm label="Remove" question="Remove build {b.variant || b.recipeId}?" danger onconfirm={() => removeBuild(b)} />
						{/snippet}
						{#snippet expand()}
							{#if b.error}<p class="error-text failure">{b.error}</p>{/if}
							<KeyRows
								dense
								rows={[
									...Object.entries(b.vars).map(([k, v]) => ({ label: k, value: v, mono: true, tip: v })),
									...Object.entries(b.facts).map(([k, v]) => ({ label: k, value: v, mono: true, tip: v })),
									{ label: 'patches', value: b.patches.join(', '), tip: b.patches.join('\n') },
									{ label: 'task', value: shortId(b.taskId), href: b.taskId ? `/tasks/${b.taskId}` : undefined, mono: true }
								]}
							/>
						{/snippet}
					</ListRow>
				{/each}
			</List>
			<LoadMore next={buildList.next} loading={buildList.loading} onmore={() => buildList.more()} />
		{/if}
	{:else if tab === 'params'}
		{#if groups.length === 0}
			<Empty icon={SlidersHorizontalIcon} text="No parameters" hint="Launches of this runtime take the model and the plan alone" />
		{:else}
			<Stack gap={24}>
				{#each groups as g (g.title)}
					<Section title={g.title} count={g.plain.length + g.advanced.length} level={3}>
						<List head={{ primary: 'Parameter', secondary: 'Default', facts: ['Range', 'Set by'] }}>
							{#each g.plain as p (p.name)}{@render paramRow(p)}{/each}
							{#if advancedOpen[g.title] || g.plain.length === 0}
								{#each g.advanced as p (p.name)}{@render paramRow(p)}{/each}
							{/if}
						</List>
						{#if g.plain.length && g.advanced.length}
							<ShowAll total={g.plain.length + g.advanced.length} shown={g.plain.length} bind:open={() => advancedOpen[g.title] ?? false, (v) => (advancedOpen[g.title] = v)} />
						{/if}
					</Section>
				{/each}
			</Stack>
		{/if}
	{/if}
</div>

<style>
	.body {
		display: flex;
		flex-direction: column;
		min-width: 0;
		margin-top: var(--nebu-space-3);
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
