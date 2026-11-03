# Mesh

Membership, members and links, formations, admissions, ties to other meshes.

Route: `/mesh/nodes`, `/mesh/links`, `/mesh/formations`, `/mesh/nearby`, `/mesh/federation`, `/mesh/tuning`. An id after nodes or formations opens its panel.

## Not in a mesh

```
 No mesh                                         listening 0.0.0.0:8485

 [Create a mesh]        [Join with a token]

 Nearby meshes
   home        3 members                          [Ask]
 Nearby nodes
   lab-05      10.0.0.15        no mesh
 Admissions
   home via lab-02     pending    expires in 52m
```

Create: name Text 64, then the join token shown once with copy and expiry. Join: token Text. An invitation row has Accept and Decline.

## Header in a mesh

```
 home  3 members lab-01 (you) · issuer cert 61 d (i) [Token] [Leave] ···
```

Name, members, this node marked you with an issuer badge, certificate days left in warning colour under thirty. `(i)` popover: id, address, exposure, announce, CA fingerprint with copy. Token: a popover with TTL Duration up to a month and Copy. Menu: Rotate authority, Hand off issuer, Reset mesh, each confirmed by name.

## Nodes

```
 ●  lab-01   you · issuer    0.9.3    2 GPU    [Open]
 ●  lab-02                   0.9.3    4 GPU    [Open]  ···
 ○  lab-03   unreachable     0.9.2    1 GPU    [Open]  ···
```

Rows: state dot, name (24ch), badges, version, device count, Open, menu: Forget. Tooltip: address, os and arch, seen at. Expand: conditions.

**Panel.** Tabs: Profile (memory bars read only, devices rows, storage rows), Installs (rows: runtime, version), Stored (rows: repository 40ch, size), Throughput (rows: device, tokens per second class, samples, source), Links (rows: to node, class pill, rtt, bandwidth), Serving (routes and pools rows).

## Links

Rows per node: name, worst link class pill, Probe. Expand: one row per peer with class pill, rtt, stream bandwidth. Tooltip: p95 rtt, aggregate, interface and speed, rdma device, mtu, subnet, measured at, held. Probe all with a force Switch in its popover.

## Formations

```
 ●  deepseek-v3    lockstep    3 nodes    42 tok/s    [Open]  ···
```

Rows: state dot, name, shape, node count, tokens per second, Open, menu: Stop, Delete, Logs. Tooltip: model, runtime, conductor, endpoint, created.

**Panel.** Tabs: Seats (rows: node, role and rank, state dot, layers; tooltip: devices and bytes, transport, address; expand: measurements, triage), Plan (chosen candidate line, then rows per candidate: shape, nodes, verdict pill, tokens per second, speedup; tooltip: score, prefill, decode, reason; ratios and sources under a collapse), Memory (bars per seat), Conditions, Request (collapse).

Plan a formation opens the Serve wizard at Placement in mesh mode without launching.

## Nearby

Nodes rows: name, address, version, membership word, Invite when outside. Invite by address: Text. Meshes rows with Ask when this node is outside. Admissions rows: side word, state pill, node, expires. Pending ones: Admit, Refuse. Settled: Dismiss. Tooltip: by, detail, created.

## Federation

Export token: TTL Duration and Copy in a popover. Import: token Text. Rows per federation node: role pill, mesh name, address, since, Drop. Tooltip: fingerprint, hash. Pools involved link to Gateway.

## Tuning

Device profiles rows: pattern (24ch), stream bandwidth, flops, fixed seconds, builtin or custom pill, menu: Edit, Delete. Editor: pattern Text 64, stream Number with unit, flops Number with unit, fixed seconds Number. Ratios rows read only: shape, runtime, link class pill, TTFT and TPT ratio, samples.

## Fields

| Field | Component | Source |
| --- | --- | --- |
| mesh name | Text | 64 |
| join token, import token | Text | |
| token ttl | Duration | up to 31 days |
| hand off target | Picker | `ListNodes` |
| invite address | Text | host and port |
| probe force | Switch | |

## Widths

Ultrawide: Split per tab. Standard: drawers. Mobile: badges and version move into the expand, the header wraps to two lines.

## Data

`GetMesh`, `Discover`, `ListNodes`, `Init`, `Join`, `Leave`, `Token`, `Rotate`, `HandOffIssuer`, `ForgetNode`, `ResetMesh`, `Probe`, `Invite`, `Ask`, `Admit`, `Refuse`, `Dismiss`, `PlanFormation`, `RunFormation`, `StopFormation`, `DeleteFormation`, `FormationLogs`, `GetFormation`, `ExportToken`, `ImportMesh`, `DropFederation`, `SetDeviceProfile`, `DeleteDeviceProfile`. Stream: MESH, NODE, FORMATION, POOL, ROUTE.
