<script lang="ts">
  import { onMount } from 'svelte';
  import { api, message } from '$lib/api';
  import { live, meshNodes, nodeName, orderedFormations, formationLive } from '$lib/state.svelte';
  import { plural } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { DeviceKind } from '$proto/host_pb';
  import { NodeState, AdmissionSide, AdmissionState, type DeviceProfile, type FormationRatio, type Formation, type Node, type NearbyNode, type Admission } from '$proto/mesh_pb';
  import { Plus, LogIn, LogOut, KeyRound, RefreshCw, Copy as CopyIcon, Send, UserPlus, Eraser } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Dialog from '$lib/components/ui/Dialog.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import TextInput from '$lib/components/ui/TextInput.svelte';
  import TextArea from '$lib/components/ui/TextArea.svelte';
  import Checkbox from '$lib/components/ui/Checkbox.svelte';
  import Copy from '$lib/components/ui/Copy.svelte';
  import NumberInput from '$lib/components/ui/NumberInput.svelte';
  import Tabs from '$lib/components/ui/Tabs.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import Disclosure from '$lib/components/ui/Disclosure.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import NearbyMeshes, { type Candidate } from '$lib/components/mesh/NearbyMeshes.svelte';
  import NearbyNodes from '$lib/components/mesh/NearbyNodes.svelte';
  import Admissions from '$lib/components/mesh/Admissions.svelte';
  import NodeCard from '$lib/components/mesh/NodeCard.svelte';
  import LinksTable from '$lib/components/mesh/LinksTable.svelte';
  import FormationsTable from '$lib/components/mesh/FormationsTable.svelte';
  import Performance from '$lib/components/mesh/Performance.svelte';

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
    const rows = new Map<string, Candidate>();
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
    const yes = await confirm({ title: `Leave ${mesh?.name}?`, message: 'Formations this node coordinates stop, and its formation records are removed.', action: 'Leave mesh', tone: 'bad' });
    if (!yes) return;
    try {
      await api.mesh.leave({});
      ok('Left the mesh');
    } catch (err) {
      fail(err, 'Could not leave mesh');
    }
  }

  async function rotate() {
    const yes = await confirm({ title: 'Rotate mesh secret?', message: 'Connected members update on their own. Unreachable members must rejoin.', action: 'Rotate secret', tone: 'warn' });
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
      message: 'Leaves the mesh, stops the formations this node coordinates, and forgets every member, link, and formation. The node keeps its identity.',
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
          ? 'The node is still reachable and returns on its next sync unless it leaves or the secret is rotated.'
          : 'Every reachable member forgets the node, its links, and its formations. It can join again later.',
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
      else ok(a?.meshName ? `Request sent to ${a.meshName}` : 'Join request sent', 'Joins once a member approves.');
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
    const yes = await confirm({ title: `Remove profile for ${p.pattern}?`, message: 'Matching devices fall back to the default numbers.', action: 'Remove', tone: 'bad' });
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
    <p>Set <code>mesh.listen</code> to a free network address.</p>
  </div>
{:else if status && !status.address}
  <div class="note note-warn mb-6">
    <p>Other nodes cannot reach this one.</p>
    <p>Set <code>mesh.listen</code> to a network address such as <code>0.0.0.0:8485</code>, or <code>mesh.advertise</code> to an address they can reach.</p>
  </div>
{/if}

{#if !loaded}
  <div class="skeleton h-40" aria-busy="true"></div>
{:else if !mesh}
  <div class="flex flex-col gap-8">
    <NearbyMeshes {candidates} announce={!!status?.announce} {acting} onask={(hash) => ask(hash)} onrefuse={refuse} ondismiss={dismiss} />
    {#if outside.length + elsewhere.length > 0}
      <NearbyNodes nodes={[...outside, ...elsewhere]} waiting={[]} member={false} {acting} oninvite={invite} />
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
        <Admissions {waiting} {pendingCount} {acting} onadmit={admit} onrefuse={refuse} oninvite={invite} ondismiss={dismiss} />
      {/if}
      <Section title="Nodes" count={nodes.length || undefined}>
        <div class="flex flex-col gap-4">
          {#each nodes as n (n.id)}
            <NodeCard {n} {acting} onforget={forgetNode} />
          {/each}
        </div>
      </Section>
      {#if outside.length + elsewhere.length > 0}
        <NearbyNodes nodes={[...outside, ...elsewhere]} {waiting} member={true} {acting} oninvite={invite} />
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
      <LinksTable {nodes} {links} {probing} onprobe={probe} />
    {:else if pane === 'formations'}
      <FormationsTable {formations} {acting} ondelete={deleteFormation} />
    {:else if pane === 'performance'}
      <Performance profiles={profileRows} {profileUse} {ratios} ondelete={deleteProfile} onadd={() => (profileOpen = true)} />
    {/if}
  </div>
{/if}

<Dialog bind:open={initOpen} title="Create mesh" size="sm">
  <div class="flex flex-col gap-4">
    <Field label="Name" for="mesh-name"><TextInput id="mesh-name" bind:value={initName} empty="home" /></Field>
    <Checkbox bind:checked={initTls} label="Encrypt mesh traffic with TLS" />
  </div>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (initOpen = false)}>Cancel</Button>
    <span class="ml-auto"><Button variant="primary" icon={Plus} loading={busy} onclick={init}>Create mesh</Button></span>
  {/snippet}
</Dialog>

<Dialog bind:open={joinOpen} title="Join mesh" size="sm">
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

<Dialog bind:open={inviteOpen} title="Invite node" size="sm">
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
  <p class="mt-3 text-xs leading-5 text-fg-muted">The token admits any node that has it.</p>
  {#snippet footer()}
    <span class="ml-auto"><Button variant="primary" icon={CopyIcon} onclick={() => { navigator.clipboard?.writeText(token); tokenOpen = false; }}>Copy and close</Button></span>
  {/snippet}
</Dialog>

<Dialog bind:open={profileOpen} title="Device profile" description="Planning numbers for devices whose name holds the pattern." size="sm">
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
