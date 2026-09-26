<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { api, message } from '$lib/api';
  import { live, clock, meshNodes, nodeName, orderedFormations, formationLive } from '$lib/state.svelte';
  import { classLabel, classTone, nodeStateLabel, nodeTone, gbits, gbytes, tflops, micros, shapeLabel, seatLabel, tps, admissionLabel, admissionTone, admissionWhat } from '$lib/mesh';
  import { ago, count, plural, when } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { DeviceKind } from '$proto/host_pb';
  import { FormationState, NodeState, AdmissionSide, AdmissionState, type DeviceProfile, type FormationRatio, type Formation, type Node, type NearbyNode, type NearbyMesh, type Admission } from '$proto/mesh_pb';
  import { Network, Plus, LogIn, LogOut, KeyRound, RefreshCw, Radar, Copy as CopyIcon, Check, X, Send, UserPlus, UserX, Trash2, RotateCcw, Eraser } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Chip from '$lib/components/ui/Chip.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Dialog from '$lib/components/ui/Dialog.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import TextInput from '$lib/components/ui/TextInput.svelte';
  import TextArea from '$lib/components/ui/TextArea.svelte';
  import Checkbox from '$lib/components/ui/Checkbox.svelte';
  import Copy from '$lib/components/ui/Copy.svelte';
  import NumberInput from '$lib/components/ui/NumberInput.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Tabs from '$lib/components/ui/Tabs.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import Disclosure from '$lib/components/ui/Disclosure.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import Devices from '$lib/components/Devices.svelte';

  let profiles = $state<DeviceProfile[]>([]);
  let ratios = $state<FormationRatio[]>([]);
  let error = $state('');
  let initOpen = $state(false);
  let joinOpen = $state(false);
  let inviteOpen = $state(false);
  let joinMethod = $state('address');
  let pane = $state('nodes');
  let tokenOpen = $state(false);
  let profileOpen = $state(false);
  let initName = $state('');
  let initTls = $state(true);
  let joinToken = $state('');
  let token = $state('');
  let busy = $state(false);
  let probing = $state(false);
  let rotating = $state(false);
  let pattern = $state('');
  let stream = $state('');
  let compute = $state('');
  let address = $state('');
  // Admission ids with a call in flight, so their buttons wait
  let acting = $state(new Set<string>());

  const status = $derived(live.mesh);
  const loaded = $derived(live.ready || !!status);
  const mesh = $derived(status?.mesh ?? null);
  const nodes = $derived(meshNodes());
  const formations = $derived(orderedFormations());
  const links = $derived(nodes.flatMap((n) => n.links));
  // Which mesh devices each profile row prices: the first pattern a device's name holds, else its kind row
  const profileUse = $derived.by(() => {
    const use = new Map<string, { key: string; label: string }[]>();
    for (const n of nodes) {
      for (const [i, d] of (n.profile?.devices ?? []).entries()) {
        const name = d.name.toLowerCase();
        const kind = 'kind:' + DeviceKind[d.kind].toLowerCase();
        const row = profiles.find((p) => !p.pattern.startsWith('kind:') && name.includes(p.pattern.toLowerCase())) ?? profiles.find((p) => p.pattern === kind);
        if (!row) continue;
        use.set(row.pattern, [...(use.get(row.pattern) ?? []), { key: `${n.id}/${i}`, label: `${n.name || n.id.slice(0, 8)} ${d.name}` }]);
      }
    }
    return use;
  });
  const profileRows = $derived([...profiles].sort((a, b) => Number(profileUse.has(b.pattern)) - Number(profileUse.has(a.pattern))));
  const others = $derived(nodes.filter((n) => !n.self));
  const nearby = $derived(status?.nodes ?? []);
  const admissions = $derived(status?.admissions ?? []);
  // Nodes heard that are in no mesh, so a member can invite them, and those in another mesh
  const outside = $derived(nearby.filter((n) => !n.member && !n.meshHash));
  const elsewhere = $derived(nearby.filter((n) => !n.member && !!n.meshHash));
  // Outside a mesh: every mesh heard, and every mesh that invited this node, one row each
  const candidates = $derived.by(() => {
    const rows = new Map<string, { hash: string; mesh?: NearbyMesh; admission?: Admission }>();
    for (const m of status?.meshes ?? []) rows.set(m.hash, { hash: m.hash, mesh: m });
    for (const a of admissions) {
      if (a.side !== AdmissionSide.CANDIDATE) continue;
      const key = a.meshHash || a.id;
      rows.set(key, { ...(rows.get(key) ?? { hash: key }), admission: a });
    }
    return [...rows.values()].sort((a, b) => (a.mesh?.name ?? a.admission?.meshName ?? '').localeCompare(b.mesh?.name ?? b.admission?.meshName ?? ''));
  });
  const waiting = $derived(admissions.filter((a) => a.side === AdmissionSide.MEMBER));
  const pendingCount = $derived(waiting.filter((a) => a.state === AdmissionState.PENDING && a.asked).length);

  async function refresh() {
    try {
      const r = await api.mesh.getMesh({});
      profiles = r.profiles;
      ratios = r.ratios;
      error = '';
    } catch (err) {
      error = message(err);
    }
  }

  onMount(() => {
    void refresh();
  });
  // The learned tables come with the mesh, so they refresh when membership changes.
  let seenMesh = '';
  $effect(() => {
    const id = mesh?.id ?? '';
    if (seenMesh === id) return;
    seenMesh = id;
    void refresh();
  });

  function hold(id: string, on: boolean) {
    const next = new Set(acting);
    if (on) next.add(id);
    else next.delete(id);
    acting = next;
  }

  async function init() {
    busy = true;
    try {
      const r = await api.mesh.init({ name: initName.trim(), tls: initTls });
      token = r.token;
      initOpen = false;
      ok(`Created ${r.mesh?.name}`, 'Invite nodes to start sharing models.');
    } catch (err) {
      fail(err, 'Could not create mesh');
    } finally {
      busy = false;
    }
  }

  async function join() {
    busy = true;
    try {
      const r = await api.mesh.join({ token: joinToken.trim() });
      joinOpen = false;
      ok(`Joined ${r.mesh?.name}`, r.warnings.join('. ') || plural(r.mesh?.members ?? 0, 'member'));
    } catch (err) {
      fail(err, 'Could not join mesh');
    } finally {
      busy = false;
    }
  }

  async function showToken() {
    try {
      const r = await api.mesh.token({});
      token = r.token;
      tokenOpen = true;
    } catch (err) {
      fail(err, 'Could not load join token');
    }
  }

  async function leave() {
    const yes = await confirm({ title: `Leave ${mesh?.name}?`, message: 'This node will disconnect from the mesh. Formations it coordinates stop, and every formation record on this node is removed.', action: 'Leave mesh', tone: 'bad' });
    if (!yes) return;
    try {
      await api.mesh.leave({});
      ok('Left the mesh');
    } catch (err) {
      fail(err, 'Could not leave mesh');
    }
  }

  async function rotate() {
    const yes = await confirm({ title: 'Rotate mesh secret?', message: 'Connected members will update automatically. Unreachable members will need to rejoin.', action: 'Rotate secret', tone: 'warn' });
    if (!yes) return;
    rotating = true;
    try {
      const r = await api.mesh.rotate({});
      ok('Mesh secret rotated', `${plural(r.rotated.length, 'member')} updated.${r.missed.length ? ` Must rejoin: ${r.missed.map(nodeName).join(', ')}.` : ''}`);
    } catch (err) {
      fail(err, 'Could not rotate mesh secret');
    } finally {
      rotating = false;
    }
  }

  async function resetMesh() {
    const yes = await confirm({
      title: mesh ? `Reset ${mesh.name}?` : 'Clear mesh data?',
      message: 'Leaves the mesh, stops every formation this node coordinates, and forgets every member, link, and formation record. The node keeps its identity.',
      action: mesh ? 'Reset mesh' : 'Clear data',
      tone: 'bad'
    });
    if (!yes) return;
    busy = true;
    try {
      await api.mesh.resetMesh({});
      ok(mesh ? 'Mesh reset' : 'Mesh data cleared');
    } catch (err) {
      fail(err, 'Could not reset mesh');
    } finally {
      busy = false;
    }
  }

  async function forgetNode(n: Node) {
    const name = n.name || n.id.slice(0, 8);
    const yes = await confirm({
      title: `Forget ${name}?`,
      message:
        n.state === NodeState.READY
          ? 'The node is still reachable. Every member forgets it, but it returns on its next sync unless it leaves or the mesh secret is rotated.'
          : 'Every reachable member forgets the node, its links, and the formations it coordinated. It can join again later.',
      action: 'Forget node',
      tone: 'bad'
    });
    if (!yes) return;
    hold(n.id, true);
    try {
      await api.mesh.forgetNode({ nodeId: n.id, tell: true });
      ok(`Forgot ${name}`);
    } catch (err) {
      fail(err, `Could not forget ${name}`);
    } finally {
      hold(n.id, false);
    }
  }

  async function deleteFormation(f: Formation) {
    const yes = await confirm({ title: `Delete ${f.name}?`, message: formationLive(f) ? 'Stops every worker first, then removes the record.' : 'Removes the record from this node.', action: 'Delete', tone: 'bad' });
    if (!yes) return;
    hold(f.id, true);
    try {
      await api.mesh.deleteFormation({ id: f.id });
      ok(`Deleted ${f.name}`);
    } catch (err) {
      fail(err, `Could not delete ${f.name}`);
    } finally {
      hold(f.id, false);
    }
  }

  async function probe() {
    probing = true;
    try {
      const r = await api.mesh.probe({ force: true });
      ok(`Measured ${plural(r.links.length, 'link')}`);
    } catch (err) {
      fail(err, 'Could not measure links');
    } finally {
      probing = false;
    }
  }

  async function invite(n?: NearbyNode) {
    const key = n?.id ?? 'address';
    hold(key, true);
    try {
      const r = await api.mesh.invite({ nodeId: n?.id ?? '', address: n ? '' : address.trim() });
      ok(`Invited ${r.admission?.node?.name || n?.name || address}`, 'Accept the invitation on that node’s Mesh page.');
      if (!n) {
        address = '';
        inviteOpen = false;
      }
    } catch (err) {
      fail(err, 'Invitation not sent');
    } finally {
      hold(key, false);
    }
  }

  async function ask(hash: string, viaAddress = '') {
    const key = hash || 'address';
    hold(key, true);
    try {
      const r = await api.mesh.ask({ meshHash: hash, nodeId: '', address: viaAddress });
      const a = r.admission;
      if (a?.state === AdmissionState.ADMITTED || a?.state === AdmissionState.JOINED) ok(`Joining ${a.meshName}`);
      else ok(a?.meshName ? `Request sent to ${a.meshName}` : 'Join request sent', 'This node will join when a mesh member approves.');
      if (viaAddress) {
        address = '';
        joinOpen = false;
      }
    } catch (err) {
      fail(err, 'Request not sent');
    } finally {
      hold(key, false);
    }
  }

  async function admit(a: Admission) {
    hold(a.id, true);
    try {
      const r = await api.mesh.admit({ admissionId: a.id });
      if (r.admission?.state === AdmissionState.JOINED) ok(`${a.node?.name} joined`);
      else ok(`Approved ${a.node?.name}`, 'The node is joining the mesh.');
    } catch (err) {
      fail(err, `Could not approve ${a.node?.name}`);
    } finally {
      hold(a.id, false);
    }
  }

  async function refuse(a: Admission) {
    const member = a.side === AdmissionSide.MEMBER;
    const yes = await confirm(
      member
        ? { title: a.asked ? `Deny ${a.node?.name}’s request?` : `Cancel invitation to ${a.node?.name}?`, message: 'The node can request to join again.', action: a.asked ? 'Deny request' : 'Cancel invitation', tone: 'bad' }
        : { title: a.asked ? `Cancel request to join ${a.meshName}?` : `Decline invitation from ${a.meshName}?`, message: 'You can request to join again later.', action: a.asked ? 'Cancel request' : 'Decline', tone: 'bad' }
    );
    if (!yes) return;
    hold(a.id, true);
    try {
      await api.mesh.refuse({ admissionId: a.id });
    } catch (err) {
      fail(err, 'Could not update request');
    } finally {
      hold(a.id, false);
    }
  }

  async function dismiss(a: Admission) {
    hold(a.id, true);
    try {
      await api.mesh.dismiss({ admissionId: a.id });
    } catch (err) {
      fail(err, 'Could not dismiss');
    } finally {
      hold(a.id, false);
    }
  }

  async function deleteProfile(p: DeviceProfile) {
    const yes = await confirm({ title: `Remove profile for ${p.pattern}?`, message: 'Matching devices will use the default performance estimates.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      const r = await api.mesh.deleteDeviceProfile({ pattern: p.pattern });
      profiles = r.profiles;
      ok(`Removed ${p.pattern}`);
    } catch (err) {
      fail(err, 'Could not remove profile');
    }
  }

  async function setProfile() {
    busy = true;
    try {
      const r = await api.mesh.setDeviceProfile({ profile: { pattern: pattern.trim(), streamBytesPerSecond: parseFloat(stream) * 1e9, computeFlops: parseFloat(compute || '0') * 1e12, fixedSeconds: 0, builtin: false } });
      profiles = r.profiles;
      profileOpen = false;
      pattern = stream = compute = '';
      ok('Profile saved');
    } catch (err) {
      fail(err, 'Could not save profile');
    } finally {
      busy = false;
    }
  }

  function gpus(n: Node): number {
    return n.profile?.devices.filter((d) => d.kind !== DeviceKind.CPU).length ?? 0;
  }

  function runtimesOf(n: Node): string[] {
    return [...new Set(n.installs.map((i) => i.runtimeId))].sort();
  }

  function pending(a: Admission | undefined): boolean {
    return a?.state === AdmissionState.PENDING;
  }

  function settledState(a: Admission | undefined): boolean {
    return !!a && a.state !== AdmissionState.PENDING && a.state !== AdmissionState.ADMITTED;
  }
</script>

<PageHeader title={mesh?.name || 'Mesh'}>
  {#snippet meta()}
    {#if mesh}
      <span>{plural(mesh.members, 'member')}</span>
      <span>{mesh.tls ? 'TLS enabled' : 'TLS disabled'}</span>
    {:else if status}
      <span>{status.nodeName}</span>
      {#if status.address}<span class="font-mono">{status.address}</span>{:else if status.listen}<span class="font-mono">{status.listen}</span>{/if}
    {/if}
    {#if status && !status.announce}<span>Discovery off</span>{/if}
  {/snippet}
  {#if mesh}
    <Button icon={KeyRound} onclick={showToken}>Join token</Button>
    <Button variant="primary" icon={UserPlus} onclick={() => (inviteOpen = true)}>Invite node</Button>
    <Menu items={[
      { label: 'Rotate secret', icon: RefreshCw, disabled: rotating, onSelect: rotate },
      { label: 'Leave mesh', icon: LogOut, tone: 'bad', onSelect: leave },
      { label: 'Reset mesh', icon: Eraser, tone: 'bad', disabled: busy, onSelect: resetMesh }
    ]} />
  {:else if loaded}
    <Button icon={LogIn} onclick={() => (joinOpen = true)}>Join mesh</Button>
    <Button variant="primary" icon={Plus} onclick={() => (initOpen = true)}>Create mesh</Button>
    {#if formations.length}<Menu items={[{ label: 'Clear mesh data', icon: Eraser, tone: 'bad', disabled: busy, onSelect: resetMesh }]} />{/if}
  {/if}
</PageHeader>

{#if error}<div class="note note-bad mb-6">{error}</div>{/if}
{#if status?.listenError}
  <div class="note note-bad mb-6">
    <p>Other nodes cannot connect: {status.listenError}</p>
    <p>Set <code>mesh.listen</code> to an available network address in the daemon configuration.</p>
  </div>
{:else if status && !status.address}
  <div class="note note-warn mb-6">
    <p>This node is only reachable locally.</p>
    <p>Set <code>mesh.listen</code> to a network address, such as <code>0.0.0.0:8485</code>, or set <code>mesh.advertise</code> to an address other nodes can reach.</p>
  </div>
{/if}

{#if !loaded}
  <div class="skeleton h-40" aria-busy="true"></div>
{:else if !mesh}
  <div class="flex flex-col gap-8">
    <Section title="Nearby meshes" count={candidates.length || undefined}>
      {#if candidates.length === 0}
        <Empty compact icon={Network} title={status?.announce ? 'No meshes found' : 'Network discovery is off'} description="Create a mesh to connect your machines, or join one with a member address or token." />
      {:else}
        <div class="flex flex-col gap-4">
          {#each candidates as c (c.hash)}
            {@const a = c.admission}
            {@const name = c.mesh?.name || a?.meshName || c.hash.slice(0, 12)}
            {@const members = c.mesh?.members || a?.meshMembers || 0}
            {@const tls = c.mesh?.tls ?? a?.meshTls ?? false}
            <Card title={name} meta={`${plural(members, 'member')} · ${tls ? 'TLS enabled' : 'TLS disabled'}`}>
              {#snippet actions()}
                {#if a}<State tone={admissionTone(a.state)} label={admissionLabel(a)} pulse={a.state === AdmissionState.ADMITTED} />{/if}
              {/snippet}
              <div class="flex flex-col gap-3">
                {#if c.mesh}
                  <div class="flex flex-wrap items-center gap-1.5 text-xs text-fg-muted">
                    <span>Members</span>
                    {#each c.mesh.nodes as n (n.id)}<Chip text={n.name || n.id.slice(0, 8)} mono={false} title={n.address} />{/each}
                    <span class="text-fg-faint" title={when(c.mesh.heardAt)}>{ago(c.mesh.heardAt, clock.now)}</span>
                  </div>
                {:else if a?.node}
                  <div class="text-xs text-fg-muted">Member: {a.node.name} <span class="font-mono">{a.node.address}</span></div>
                {/if}
                {#if a}
                  {#if a.by}<p class="text-xs text-fg-muted">{admissionWhat(a)} · {a.by}</p>{/if}
                  {#if a.detail}<p class="text-sm leading-6 text-fg-muted">{a.detail}</p>{/if}
                {/if}
                <div class="flex flex-wrap items-center gap-2">
                  {#if a && a.invited && !a.asked && pending(a)}
                    <Button variant="primary" size="sm" icon={Check} loading={acting.has(c.hash)} onclick={() => ask(c.hash)}>Accept invitation</Button>
                    <Button size="sm" icon={X} loading={acting.has(a.id)} onclick={() => refuse(a)}>Decline</Button>
                  {:else if a && a.asked && pending(a)}
                    <Button size="sm" icon={X} loading={acting.has(a.id)} onclick={() => refuse(a)}>Cancel request</Button>
                  {:else if a && a.state === AdmissionState.ADMITTED}
                    <span class="text-sm text-fg-muted">Connecting to the mesh…</span>
                  {:else}
                    <Button variant="primary" size="sm" icon={Send} loading={acting.has(c.hash)} disabled={!c.mesh && !a?.node?.address} onclick={() => ask(c.hash)}>{a && settledState(a) ? 'Request again' : 'Request to join'}</Button>
                    {#if a && settledState(a)}<Button size="sm" icon={Trash2} loading={acting.has(a.id)} onclick={() => dismiss(a)}>Dismiss</Button>{/if}
                  {/if}
                </div>
              </div>
            </Card>
          {/each}
        </div>
      {/if}
    </Section>

    {#if outside.length + elsewhere.length > 0}
    <Section title="Nearby nodes" count={outside.length + elsewhere.length}>
        <div class="tbl-wrap">
          <table class="tbl dense">
            <thead><tr><th>Node</th><th>Address</th><th>Mesh</th><th>System</th><th>Last seen</th></tr></thead>
            <tbody>
              {#each [...outside, ...elsewhere] as n (n.id)}
                <tr>
                  <td class="text-fg" title={n.id}>{n.name || n.id.slice(0, 12)}</td>
                  <td class="font-mono text-xs text-fg-muted">{n.address || 'Local only'}</td>
                  <td class="text-fg-muted">{n.meshName || '–'}</td>
                  <td class="text-fg-muted">{n.os}/{n.arch}{#if n.version} · nebu {n.version}{/if}</td>
                  <td class="text-fg-muted" title={when(n.heardAt)}>{ago(n.heardAt, clock.now)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        {#if outside.length}<p class="text-sm text-fg-muted">Create a mesh to invite these nodes.</p>{/if}
    </Section>
    {/if}
  </div>
{:else}
  <Tabs
    class="mb-6 overflow-x-auto"
    bind:value={pane}
    tabs={[
      { id: 'nodes', label: 'Nodes', count: nodes.length },
      { id: 'formations', label: 'Formations', count: formations.length || undefined },
      { id: 'links', label: 'Links' },
      { id: 'performance', label: 'Performance' }
    ]}
  />
  <div class="flex flex-col gap-10">
    {#if pane === 'nodes'}
    {#if waiting.length > 0}
    <Section title="Requests and invitations" count={pendingCount || undefined}>
        <div class="tbl-wrap">
          <table class="tbl">
            <thead><tr><th>Node</th><th>Type</th><th>Status</th><th>Updated</th><th class="actions"></th></tr></thead>
            <tbody>
              {#each waiting as a (a.id)}
                <tr>
                  <td class="text-fg">
                    {a.node?.name || a.node?.id.slice(0, 12) || '–'}
                    {#if a.node?.address}<div class="font-mono text-xs text-fg-muted">{a.node.address}</div>{/if}
                  </td>
                  <td class="text-fg-muted">{admissionWhat(a)}</td>
                  <td>
                    <State tone={admissionTone(a.state)} label={admissionLabel(a)} pulse={a.state === AdmissionState.ADMITTED} />
                    {#if a.detail}<p class="mt-1 max-w-sm text-xs leading-5 text-fg-muted">{a.detail}</p>{/if}
                  </td>
                  <td class="text-xs text-fg-muted" title={[when(a.updatedAt), a.by].filter(Boolean).join(' · ')}>{ago(a.updatedAt, clock.now)}</td>
                  <td class="actions">
                    <div class="flex justify-end gap-2">
                      {#if pending(a) && a.asked}
                        <Button variant="primary" size="sm" icon={Check} loading={acting.has(a.id)} onclick={() => admit(a)}>Approve</Button>
                        <Button size="sm" icon={X} loading={acting.has(a.id)} onclick={() => refuse(a)}>Deny</Button>
                      {:else if pending(a)}
                        <Button size="sm" icon={X} loading={acting.has(a.id)} onclick={() => refuse(a)}>Cancel invitation</Button>
                      {:else if a.state === AdmissionState.ADMITTED}
                        <span class="self-center text-xs text-fg-muted">Joining</span>
                      {:else}
                        {#if a.state === AdmissionState.FAILED || a.state === AdmissionState.EXPIRED}
                          <Button size="sm" icon={RotateCcw} loading={acting.has(a.id)} onclick={() => (a.asked ? admit(a) : invite(a.node))}>Try again</Button>
                        {/if}
                        <Button size="sm" icon={Trash2} loading={acting.has(a.id)} onclick={() => dismiss(a)}>Dismiss</Button>
                      {/if}
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
    </Section>
    {/if}

    <Section title="Nodes" count={nodes.length || undefined}>
      <div class="flex flex-col gap-4">
        {#each nodes as n (n.id)}
          <Card title={n.name || n.id.slice(0, 12)} meta={n.address || undefined}>
            {#snippet actions()}
              <State tone={nodeTone(n)} label={nodeStateLabel(n)} pulse={!n.self && n.state === 0} />
              {#if !n.self}<IconButton size="xs" icon={UserX} label="Forget node" loading={acting.has(n.id)} onclick={() => forgetNode(n)} />{/if}
            {/snippet}
            <div class="flex flex-col gap-4">
              <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-fg-muted">
                <span class="font-mono">{n.os}/{n.arch}</span>
                {#if n.version}<span>nebu {n.version}</span>{/if}
                <span>{plural(gpus(n), 'accelerator')}</span>
                <span>{plural(n.stored.length, 'model')}</span>
                {#if !n.self}<span title={when(n.seenAt)}>Seen {ago(n.seenAt, clock.now)}</span>{/if}
              </div>
              {#if n.profile}
                <Devices host={n.profile} />
              {:else}
                <p class="text-sm text-fg-muted">Waiting for device information</p>
              {/if}
              <div class="flex flex-wrap items-center gap-1.5">
                {#each runtimesOf(n) as rt (rt)}<Chip text={rt} mono={false} />{/each}
              </div>
              <Disclosure label="Node details">
              <div class="flex flex-col gap-4">
                <Kv items={[
                  ['Node ID', n.id],
                  ['Address', n.address],
                  ['Version', n.version]
                ]} />
                {#if n.capabilities.length}
                  <div class="tbl-wrap">
                    <table class="tbl dense">
                      <thead><tr><th>Runtime</th><th>Distributions</th></tr></thead>
                      <tbody>
                        {#each n.capabilities as c (c.installId)}
                          <tr>
                            <td class="font-mono text-xs">{c.runtimeId} <span class="text-fg-muted">{c.version}</span></td>
                            <td class="text-xs text-fg-muted">{c.shapes.map(shapeLabel).join(', ')}</td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  </div>
                {/if}
              {#if n.routes.length || n.slots.length || n.formationIds.length || n.seats.length}
                <div class="flex flex-wrap items-center gap-1.5 text-xs text-fg-muted">
                  {#if n.formationIds.length}<span>Coordinates {plural(n.formationIds.length, 'formation')}</span>{/if}
                  {#each n.seats as s (s.instanceId || s.formationId + s.role + s.rank)}
                    <Chip text={seatLabel(s.role, s.rank)} mono={false} title={`Formation ${s.formationId.slice(0, 8)}${s.transport ? ' · ' + s.transport : ''}`} />
                  {/each}
                  {#each n.slots as sl (sl.id)}<Chip text={sl.name} title={sl.formationId ? 'mesh slot' : 'slot'} />{/each}
                  {#each n.routes as r (r.name)}<Chip text={r.name} title={`route on ${n.name}`} class="text-fg-faint" />{/each}
                </div>
              {/if}
              {#if n.throughput.length}
                <div class="tbl-wrap">
                  <table class="tbl dense">
                    <thead><tr><th>Device</th><th class="num">Bandwidth</th><th class="num">Compute</th><th class="num">Samples</th></tr></thead>
                    <tbody>
                      {#each n.throughput as t (t.deviceId)}
                        <tr>
                          <td class="font-mono text-xs" title={t.deviceId}>{n.profile?.devices.find((d) => d.id === t.deviceId)?.name || t.deviceId}</td>
                          <td class="num">{gbytes(t.streamBytesPerSecond)}</td>
                          <td class="num">{tflops(t.computeFlops)}</td>
                          <td class="num">{count(t.samples)}</td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
              {/if}
              </div>
              </Disclosure>
            </div>
          </Card>
        {/each}
      </div>
    </Section>

    {#if outside.length + elsewhere.length > 0}
    <Section title="Nearby nodes" count={outside.length + elsewhere.length}>
        <div class="tbl-wrap">
          <table class="tbl dense">
            <thead><tr><th>Node</th><th>Address</th><th>System</th><th>Last seen</th><th class="actions"></th></tr></thead>
            <tbody>
              {#each [...outside, ...elsewhere] as n (n.id)}
                {@const a = waiting.find((x) => x.node?.id === n.id)}
                <tr>
                  <td class="text-fg" title={n.id}>{n.name || n.id.slice(0, 12)}</td>
                  <td class="font-mono text-xs text-fg-muted">{n.address || 'Local only'}</td>
                  <td class="text-fg-muted">{n.os}/{n.arch}{#if n.version} · nebu {n.version}{/if}</td>
                  <td class="text-fg-muted" title={when(n.heardAt)}>{ago(n.heardAt, clock.now)}</td>
                  <td class="actions">
                    <div class="flex justify-end">
                      {#if n.meshHash}
                        <span class="text-xs text-fg-muted">Member of {n.meshName}</span>
                      {:else if a && !settledState(a)}
                        <span class="text-xs text-fg-muted">{admissionLabel(a)}</span>
                      {:else}
                        <Button size="sm" icon={UserPlus} loading={acting.has(n.id)} disabled={!n.address} onclick={() => invite(n)}>Invite</Button>
                      {/if}
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
    </Section>
    {/if}

    <Disclosure label="Mesh details">
      <Kv class="max-w-2xl" items={[
        ['Mesh ID', mesh.id],
        ['Address', status?.address],
        ['Network discovery', status?.announce ? 'Enabled' : 'Disabled'],
        ['Worker access', mesh.exposure]
      ]} />
    </Disclosure>
    {:else if pane === 'links'}
    <Section title="Links" count={links.length || undefined}>
      {#snippet actions()}
        <Button size="sm" icon={Radar} loading={probing} disabled={!others.length} onclick={probe}>Measure links</Button>
      {/snippet}
      {#if others.length === 0}
        <Empty compact title="Add another node to measure link performance" />
      {:else}
        <div class="tbl-wrap">
          <table class="tbl dense">
            <thead>
              <tr><th>From / To</th>{#each nodes as n (n.id)}<th>{n.name || n.id.slice(0, 8)}</th>{/each}</tr>
            </thead>
            <tbody>
              {#each nodes as from (from.id)}
                <tr>
                  <td class="font-medium text-fg">{from.name || from.id.slice(0, 8)}</td>
                  {#each nodes as to (to.id)}
                    {@const l = from.id === to.id ? undefined : links.find((x) => x.from === from.id && x.to === to.id)}
                    <td>
                      {#if from.id === to.id}
                        <span class="text-fg-faint">–</span>
                      {:else if l}
                        <div class="flex flex-col gap-0.5" title={l.detail}>
                          <State tone={classTone(l.class)} label={classLabel(l.class)} />
                          <span class="whitespace-nowrap text-xs tabular-nums text-fg-muted">{micros(l.rttUs)} · {gbits(l.streamBytesPerSecond)}</span>
                          <span class="text-xs text-fg-muted">{l.interface}{#if l.bandwidthHeld} · bandwidth from the last run{/if}</span>
                          {#if l.detail}<span class="max-w-xs text-xs leading-5 text-fg-muted wrap-anywhere">{l.detail}</span>{/if}
                          <Disclosure label="Details">
                            <Kv items={[
                              ['Round trip', micros(l.rttUs)],
                              ['Single stream', gbits(l.streamBytesPerSecond)],
                              ['Four streams', gbits(l.aggregateBytesPerSecond)],
                              ['Interface speed', gbits(Number(l.interfaceBitsPerSecond) / 8)],
                              ['MTU', l.mtu || undefined],
                              ['RDMA device', l.rdmaDevice || undefined],
                              ['Subnet', l.subnet ? 'Same subnet' : 'Routed']
                            ]} omitEmpty />
                          </Disclosure>
                        </div>
                      {:else}
                        <span class="text-xs text-fg-faint">unmeasured</span>
                      {/if}
                    </td>
                  {/each}
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </Section>

    {:else if pane === 'formations'}
    <Section title="Formations" count={formations.length || undefined}>
      {#if formations.length === 0}
        <Empty compact title="No formations yet" description="Run a model and choose its mesh nodes in the Run dialog to create a formation.">
          <Button size="sm" href="/store">Browse models</Button>
        </Empty>
      {:else}
        <div class="tbl-wrap">
          <table class="tbl">
            <thead><tr><th>Name</th><th>Distribution</th><th>State</th><th>Coordinator</th><th>Nodes</th><th class="num">Est. tokens/s</th><th class="actions"></th></tr></thead>
            <tbody>
              {#each formations as f (f.id)}
                <tr class="row-link" onclick={() => goto(`/formations/${f.id}`)}>
                  <td class="font-mono text-xs text-fg">
                    <a href={`/formations/${f.id}`} class="link">{f.name}</a>
                    {#if f.error}<p class="mt-1 max-w-sm truncate font-sans text-xs text-bad" title={f.error}>{f.error}</p>{/if}
                  </td>
                  <td>{shapeLabel(f.shape)}</td>
                  <td><State values={FormationState} value={f.state} /></td>
                  <td class="text-fg-muted">{f.conductorName || nodeName(f.conductor)}</td>
                  <td>
                    <div class="flex flex-wrap gap-1.5">
                      {#each [...new Set(f.seats.map((s) => s.nodeId))] as id (id)}
                        <Chip text={f.seats.find((s) => s.nodeId === id)?.nodeName || nodeName(id)} mono={false} />
                      {/each}
                    </div>
                  </td>
                  <td class="num">{tps(f.plan?.tokensPerSecond)}</td>
                  <td class="actions"><div class="flex justify-end"><IconButton size="xs" icon={Trash2} label="Delete" loading={acting.has(f.id)} onclick={(e) => { e.stopPropagation(); deleteFormation(f); }} /></div></td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </Section>

    {:else if pane === 'performance'}
    <Section title="Device profiles" count={profiles.length || undefined}>
      {#snippet actions()}
        <Button size="sm" icon={Plus} onclick={() => (profileOpen = true)}>Add profile</Button>
      {/snippet}
      <p class="text-sm text-fg-muted">Bandwidth and compute the planner prices a device with until requests through it teach real numbers. Rows in use by this mesh come first.</p>
      <div class="tbl-wrap">
        <table class="tbl dense">
          <thead><tr><th>Device pattern</th><th>Prices</th><th class="num">Memory bandwidth</th><th class="num">Compute</th><th>Source</th><th class="actions"></th></tr></thead>
          <tbody>
            {#each profileRows as p (p.pattern)}
              <tr class={profileUse.has(p.pattern) ? 'row-active' : ''}>
                <td class="font-mono text-xs text-fg">{p.pattern}</td>
                <td class="text-xs text-fg-muted">{#each profileUse.get(p.pattern) ?? [] as d (d.key)}<div>{d.label}</div>{:else}-{/each}</td>
                <td class="num">{gbytes(p.streamBytesPerSecond)}</td>
                <td class="num">{tflops(p.computeFlops)}</td>
                <td class="text-fg-muted">{p.builtin ? 'Default' : 'Custom'}{#if !p.builtin && p.updatedAt}<span class="ml-2 text-xs" title={when(p.updatedAt)}>{ago(p.updatedAt, clock.now)}</span>{/if}</td>
                <td class="actions"><div class="flex justify-end">{#if !p.builtin}<IconButton size="xs" icon={Trash2} label="Remove" onclick={() => deleteProfile(p)} />{/if}</div></td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Section>
    {#if ratios.length}
      <Section title="Measured adjustments" count={ratios.length}>
        <p class="text-sm text-fg-muted">Multipliers applied to estimated response times, based on completed requests.</p>
        <div class="tbl-wrap">
          <table class="tbl dense">
            <thead><tr><th>Distribution</th><th>Runtime</th><th>Link class</th><th class="num">First token</th><th class="num">Per token</th><th class="num">Requests</th></tr></thead>
            <tbody>
              {#each ratios as r}
                <tr>
                  <td>{shapeLabel(r.shape)}</td>
                  <td class="font-mono text-xs">{r.runtimeId}</td>
                  <td>{classLabel(r.linkClass)}</td>
                  <td class="num">{r.ttftRatio.toFixed(2)}×</td>
                  <td class="num">{r.tptRatio.toFixed(2)}×</td>
                  <td class="num">{count(r.samples)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </Section>
    {/if}
    {/if}
  </div>
{/if}

<Dialog bind:open={initOpen} title="Create mesh" description="Connect nodes to share models and run them together." size="sm">
  <div class="flex flex-col gap-4">
    <Field label="Name" for="mesh-name"><TextInput id="mesh-name" bind:value={initName} empty="home" /></Field>
    <Checkbox bind:checked={initTls} label="Encrypt mesh traffic with TLS" />
  </div>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (initOpen = false)}>Cancel</Button>
    <span class="ml-auto"><Button variant="primary" icon={Plus} loading={busy} onclick={init}>Create mesh</Button></span>
  {/snippet}
</Dialog>

<Dialog bind:open={joinOpen} title="Join mesh" description={joinMethod === 'token' ? 'Paste a join token from a member’s Mesh page.' : 'Request to join using the address of a mesh member.'} size="sm">
  <Tabs class="mb-5" bind:value={joinMethod} tabs={[{ id: 'address', label: 'Address' }, { id: 'token', label: 'Token' }]} />
  {#if joinMethod === 'token'}
    <Field label="Join token" for="mesh-join"><TextArea id="mesh-join" mono bind:value={joinToken} height="h-28" empty="nebu-mesh-…" /></Field>
  {:else}
    <Field label="Member address" for="ask-address" description="A member must approve your request.">
      <TextInput id="ask-address" mono bind:value={address} empty="192.168.1.20:8485" onkeydown={(e) => e.key === 'Enter' && address.trim() && !acting.has('address') && ask('', address.trim())} />
    </Field>
  {/if}
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (joinOpen = false)}>Cancel</Button>
    <span class="ml-auto">
      {#if joinMethod === 'token'}
        <Button variant="primary" icon={LogIn} loading={busy} disabled={!joinToken.trim()} onclick={join}>Join mesh</Button>
      {:else}
        <Button variant="primary" icon={Send} loading={acting.has('address')} disabled={!address.trim()} onclick={() => ask('', address.trim())}>Request to join</Button>
      {/if}
    </span>
  {/snippet}
</Dialog>

<Dialog bind:open={inviteOpen} title="Invite node" description="Send an invitation to a node by its network address." size="sm">
  <Field label="Node address" for="invite-address" description="Accept the invitation on that node’s Mesh page.">
    <TextInput id="invite-address" mono bind:value={address} empty="192.168.1.30:8485" onkeydown={(e) => e.key === 'Enter' && address.trim() && !acting.has('address') && invite()} />
  </Field>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (inviteOpen = false)}>Cancel</Button>
    <span class="ml-auto"><Button variant="primary" icon={UserPlus} loading={acting.has('address')} disabled={!address.trim()} onclick={() => invite()}>Send invitation</Button></span>
  {/snippet}
</Dialog>

<Dialog bind:open={tokenOpen} title="Join token" description="On the other node, open Mesh → Join mesh → Token.">
  <div class="flex items-start gap-2">
    <pre class="code min-w-0 flex-1 whitespace-pre-wrap wrap-anywhere">{token}</pre>
    <Copy text={token} size={14} />
  </div>
  <p class="mt-3 text-xs leading-5 text-fg-muted">This token grants access to the mesh. Share it only with trusted nodes.</p>
  {#snippet footer()}
    <span class="ml-auto"><Button variant="primary" icon={CopyIcon} onclick={() => { navigator.clipboard?.writeText(token); tokenOpen = false; }}>Copy and close</Button></span>
  {/snippet}
</Dialog>

<Dialog bind:open={profileOpen} title="Device profile" description="Set initial performance estimates for matching devices." size="sm">
  <div class="flex flex-col gap-4">
    <Field label="Device name pattern" for="profile-pattern"><TextInput id="profile-pattern" mono bind:value={pattern} empty="RTX 4090" /></Field>
    <div class="grid grid-cols-2 gap-4">
      <Field label="Memory bandwidth" for="profile-stream"><NumberInput id="profile-stream" min={1} unit="GB/s" bind:value={stream} /></Field>
      <Field label="Compute" for="profile-compute"><NumberInput id="profile-compute" min={0} unit="TFLOPS" bind:value={compute} /></Field>
    </div>
  </div>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (profileOpen = false)}>Cancel</Button>
    <span class="ml-auto"><Button variant="primary" loading={busy} disabled={!pattern.trim() || !(parseFloat(stream) > 0)} onclick={setProfile}>Save profile</Button></span>
  {/snippet}
</Dialog>
