# Mesh

A mesh is a set of nebu daemons on one network that pool their devices. A model that fits no single member runs across several. A model that fits one member runs faster when the others help. Every member keeps its own store, runtimes, slots, and gateway. Joining a mesh takes nothing away from a member.

Three promises, in the order they ship:

1. Any member's UI, CLI, and gateway show and reach the whole mesh.
2. A model too large for any member runs across members, on any mix of vendors and operating systems.
3. A model that fits one member gains time to first token, tokens per second, or requests per second from the others, and the planner says which and by how much before anything launches.

The design is device and vendor neutral. Nothing in it names a machine. A link is what the probe measured, a device is what the prober found, and a runtime is what its install can do. Two identical boxes with a direct high speed cable, three mismatched desktops on a switch, and a workstation with a laptop beside it are all the same mesh with different numbers.

## Terms

| Term | Meaning |
| --- | --- |
| node | One nebu daemon. It has an identity, a host profile, a store, installs, slots, and a gateway |
| mesh | The nodes that share one secret. A node belongs to at most one mesh |
| link | The measured connection between two nodes: round trip, bandwidth, and interface facts |
| shape | A way of spreading one model over nodes: chain, lockstep, relay, replicas, draft, stages |
| formation | One model running in one shape across nodes |
| seat | One node's place in a formation: the role it plays and the process it runs |
| head | The seat that answers the route: it tokenizes, samples, and holds the route's endpoint |
| conductor | The node that owns a formation: plans it, launches its seats, holds its route, and answers for it. The head runs on the conductor |
| span | The nodes and devices a run may use. A slot with a span across nodes is a mesh slot |

## Shapes

A shape is what a formation is made of, what crosses links, and what that costs. Every shape is built from pieces nebu already has: tensor groups from the descriptor, pools from host profiles, the estimate solver's placement rules, and a runtime rendering a command. The mesh adds links between pools and a planner that prices them.

| Shape | Seats hold | Crosses links | For | Needs |
| --- | --- | --- | --- | --- |
| solo | everything on one node | nothing | what fits one node | nothing new |
| chain | consecutive layer ranges | one activation vector per token per boundary, kilobytes | a model that fits no node, on any mix of vendors | a lan link |
| lockstep | every layer, each split across all seats | two reductions per layer per token | lower time per token when matching nodes share a fabric | a fabric link, matching nodes |
| relay | the whole model on two nodes: one prefills, one decodes | the prompt's cache, once per request | lower time to first token when one node computes faster and another streams faster | a fast link |
| replicas | the whole model on every seat | whole requests | more requests per second, cache affinity | any link |
| draft | the target on the head, a draft model on another node | token ids, a few per token | more tokens per second when the head has no memory left for a draft | a lan link |
| stages | a diffusion pipeline's denoiser on one node, its encoders and decoder on another | embeddings and latents, once per request | a pipeline that fits no node, or a denoiser on the big device with everything else beside it | a lan link |

One shape answers "it does not fit": chain. Four answer "it fits, make it faster", and the planner's numbers say which applies to a given mesh: draft when the head is memory bound and slow per token, relay when one node prefills faster than the decode node and the link moves the cache faster than the compute it saves, replicas when the load is concurrent, lockstep when the link is a fabric. The published measurements behind those rules, in order of what pays most often on a home network: a draft beside a slow target gives 1.5 to 2.6 times the tokens per second, a relay between a compute rich node and a bandwidth rich node on a 10 Gb/s link gave 2.8 times on an 8k token prompt, replicas add throughput linearly, and lockstep on a fabric tops out near 1.8 times for two dense seats because every layer's two reductions are paid in latency.

What is not a shape: a cache spilled to another node's memory. At lan speeds a gigabyte of spilled cache costs most of a second per token to stream back. Experts moved to another node on a lan link cost two round trips per expert layer per token, which is a lockstep cost without lockstep's gain, so they are a chain placement on a fabric link and nothing on a lan link. Both are stated so the planner is not asked for them.

### Chain

Stage seats hold layer ranges. The head holds the embedding, the last layers, and the output, and it samples. Activations cross at every layer boundary, `d` values per token, `d × P` values per boundary for a prompt of `P` tokens. Runtimes differ in who carries them. llama.cpp's head carries every crossing itself: it sends the residual to each stage and reads it back, so the chain is a star from the head and each stage costs one blocking round trip per token. vLLM and SGLang pass rank to rank, so the chain is a ring and each boundary costs one hop. The cost model has both.

Layers follow bandwidth, not memory. Two nodes with 128 GiB each, one streaming weights at 270 GB/s and one at 800 GB/s, hold a 200 GiB model best when the faster node takes as many layers as its memory allows and the slower node takes the rest, because every layer on the slower node costs three times as long per token. Memory proportional splitting, which every cluster tool surveyed does by default, gives both nodes half, and the slower node then sets the pace. The planner fills the fastest node first, to the capacity the estimate solver computes for it with cache and overhead, then the next, then prunes: every subset of the span's nodes up to eight is a candidate, so a slow node that adds a hop and holds little is dropped rather than kept. Beyond eight nodes the planner keeps the eight fastest. Fewer, larger stages win once a hop costs milliseconds, and the project that measured this against memory proportional splitting on mixed home devices reports up to 30 times lower time per token.

Cache lives with its layers. Each stage's cache is sized with the family's per token count for its layer range at the context the plan chose, and counted against that stage's capacity, as the estimate solver does today for one node.

Prompt length decides what a boundary costs. At `d` = 8192 and a 4096 token prompt each boundary moves 128 MiB of activations, one second at 1 Gb/s and under a tenth at 25 Gb/s, so a chain on a lan link is a decode shape and the planner's prefill number says so. Runtimes that chunk the prompt and pipeline the chunks overlap this, and the runtime's overlap fact credits it. A chain that fits on two nodes but spills on one is not a speed up over a node that fits it whole: measured on one runtime, a model that fits one box decodes 25 percent slower split over two on a 10 Gb/s link. The planner scores chain against solo whenever solo fits, and solo wins.

A draft beside the head divides the chain's link cost by the tokens accepted per round, because a round verifies several tokens in one pass through the stages. Chain and a local draft compose on llama.cpp, and the planner prices the pair.

### Lockstep

Every seat holds a slice of every layer, and the seats reduce twice per layer per token. Bandwidth is not the cost: the reductions of a 70B model move under 3 MiB per token. Latency is: 160 reductions per token at 200 µs each on sockets is 32 ms before any compute, so lockstep on a lan link is slower than solo on either node. On a fabric at 40 µs per reduction it is 6 ms, and two nodes that each stream a 70B model at 273 GB/s go from about 150 ms per token solo to about 80 ms together, a 1.9 times gain, which matches what pairs of identical unified memory boxes on a direct 200 Gb/s link report. The planner requires the link class fabric and rejects the rest with the measured round trip in the reason.

Lockstep needs matching seats: same vendor and same device count per node, because the ranks are symmetric. The mesh profile carries both. Install versions may differ, the plan notes when they do, and the runtime decides whether the ranks meet. A fabric link that the collective library takes as sockets instead of the RDMA device is the most common reason a cluster is slower than one node, so the seat's log line naming the transport is parsed and shown.

### Relay

The prefill seat computes the prompt, the decode seat continues it, and the cache moves between them. What makes relay pay is asymmetry: one node has more compute per second, another more memory bandwidth per second, and a request spends its prompt on the first and its answer on the second. Two identical nodes gain nothing from relay, and the planner's relay candidate on two identical nodes scores below solo, with the reason.

The cache crossing the link is `k × P` bytes: 320 KiB per token for a 70B model with grouped query attention, so 1.25 GiB for 4096 tokens. When the runtime streams the cache layer by layer while later layers compute, the transfer hides behind compute when a layer's cache moves faster than its prefill takes, which holds for long prompts on a fast link and never on a slow one. The planner's relay line prices this exactly, and the prompt length above which relay wins appears in the candidate's reason. The gateway drives relay: it is the proxy the runtimes expect between a prefill server and a decode server, and nebu already has it.

### Replicas

Each seat serves the whole model alone. The gateway spreads requests and keeps conversations on the seat whose cache holds their prefix. Throughput adds up. Per request latency does not change, and the candidate says so, so a person asking for tokens per second is not sold replicas.

### Draft

The head runs the target model. A stage runs the draft model on its own device and the head asks it for a few tokens per round, then verifies them in one pass. Only token ids cross the link, so the round trip is paid once per draft token and the target's step, tens of milliseconds for a large model, hides it: the network is under one percent of a round on any lan link for targets slower than 50 tokens per second. The gain is the speculation itself, 1.5 to 2.6 times the tokens per second, and the shape exists for the head that fits its target with nothing left over. Draft models that read the target's hidden states, the ones the big serving engines use, must sit beside the target and are the solo speculative settings nebu already exposes. Draft elsewhere is a standalone draft model on llama.cpp.

The planner prices a round as `γ × (r + draft step on the stage) + target step on the head`, divided by the tokens a round yields, `(1 − α^(γ+1)) / (1 − α)` for an acceptance `α` per draft token, learned from the head's counters and 0.7 before the first sample.

### Stages

A diffusion pipeline is already a blueprint of slots that fill from different files. Stages puts the denoiser on the node with the fastest device and keeps the text encoders and the decoder on the head. The text embedding crosses once per request, megabytes, the latent crosses once per request, kilobytes for images and up to 15 MiB for video, and nothing crosses per step. The text encoders are the largest idle weights in a pipeline, 10 to 48 GiB used for a few seconds per request, which is why the split pays: the big device holds only what runs every step.

## Membership

A node's identity is a random 128 bit id and an Ed25519 key pair made on first start and kept in the `mesh_identity` table. A mesh is a 256 bit secret and a name. The Mesh page's Make a mesh makes them on the first node, as `nebu mesh init` does. Every other node joins from its own Mesh page, or is admitted from any member's Mesh page, without anyone copying anything between machines:

| Way | Who starts it | What happens |
| --- | --- | --- |
| ask | A node outside a mesh picks a mesh its page heard on the network and asks to join | The ask reaches one member, and every member's page shows it within a sync. Any member admits or denies. Admitting hands the node the join token over the network, and the node joins on its own |
| invite | A member picks a node its page heard on the network, in no mesh, and invites it | The node's page shows the invitation. Accepting it is an ask the invitation answers by itself: the inviting member, or any member that took the invitation over the sync, admits without deciding again. Declining tells the mesh |
| by address | Either side types the other's address, on a network that passes no beacons | The same two flows, the first contact finding whether the other side serves TLS and pinning its certificate |
| token | A member's page shows a join token; a node outside pastes it | The fallback for a node that cannot be reached at any address. A join token is the mesh name, the secret, the address of one member, and that member's TLS fingerprint when it listens with TLS |

An admission is one record per node on the member side and one per mesh on the candidate side. It says who invited and who asked, where it stands, who decided, and why it failed when it did: pending, denied, declined, expired after ten minutes with no decision, failed with the reason, admitted with the token on its way, joined. Members carry their admissions on every sync, the copy changed last winning, so a request made to one member is decided from any member's page. Settled admissions stay on the page for ten minutes and can be dismissed; a dismissed one leaves every member's page on the next sync. Leaving forgets them all. `nebu mesh nearby`, `invite`, `ask`, `admit`, `refuse`, and `dismiss` do from the CLI what the page does.

Every node, in a mesh or not, sends a UDP multicast beacon on the group every 10 s, on every interface that is up and multicast capable, and hears the others'. A beacon carries the node's id, name, listener address, TLS fingerprint, version, operating system, and architecture, and when the node belongs to a mesh, a hash of the mesh id, the mesh name, its member count, and whether it has TLS. The Mesh page lists what was heard: the meshes nearby with the members they were heard from, and the nodes nearby with the mesh each belongs to, if any. A node unheard for 45 s leaves the list. `mesh.announce: false` turns beacons off, and the page then invites and asks by address.

Members find each other three ways, all on by default:

| Way | How | When it applies |
| --- | --- | --- |
| bootstrap | The address in the join token, which admission delivers | Always, the first contact |
| gossip | Every member forwards its member list on every sync | Always, so one address reaches the whole mesh |
| announce | The beacon, when it names this node's mesh | LANs that pass multicast |

A beacon does not admit a node, and neither does an ask or an invitation by itself: a person at a page decides, once, on either side. Admission is the handshake: the caller sends its node id and a nonce, the callee answers with an HMAC of both under the secret and its own nonce, the caller answers with the HMAC of the callee's nonce. Each side then mints a session token for the other, valid for an hour and renewed on sync. Every node to node call carries its session token as a bearer credential. The daemon's guard accepts session tokens for the node to node procedures alone, so the mesh reuses the API listener, the same Connect services, the same TLS, and the same request logging, and a member's token opens nothing that decides what another node does. The ask, invitation, and token delivery calls carry no credential, since a node outside holds no session: what each may do is decided by the record on the receiving side. A token arrives only at a node that asked, and only for the mesh it asked, and the handshake proves the member that sent it holds the secret.

Node to node traffic goes over the API listener. `mesh.listen` binds a second listener when the API listener stays on loopback, which is the default install, and the default address is `0.0.0.0:8485`. The listener is bound at start whenever beacons are on, so a node outside any mesh can be invited from another node's page; when the default address cannot be bound the page says so and names the setting. `mesh.advertise` names the address the node tells others, for hosts with several interfaces, and it should be the address on the fastest link to the other members. Making or joining a mesh refuses a node whose listeners are loopback only, with the setting to change in the error.

Every member syncs with every other member every 5 s and on every change it makes. A sync carries the sender's node record and the sender's member list. A member missing for three syncs is `unreachable`. A member missing for 10 minutes is `gone` until it syncs again. Records never merge: each node is the only writer of its own record and each conductor the only writer of its formations. Every other node keeps copies keyed by node id and sequence number and takes the higher sequence. There is no leader and no election. A full mesh sync is N² messages of a few kilobytes every 5 s, nothing at the 16 stages one formation can hold and small well beyond.

## Inventory

A node record is what every other node knows about it:

| Field | Source | Refresh |
| --- | --- | --- |
| identity, name, addresses, version, os, arch | daemon | on change |
| host profile: devices, pools, storage, facts | the prober, as `nebu host` shows it | every probe, 30 s |
| installs: runtime id, version, facts | the installs manager | on change |
| stored: source, repo, group, format, bytes, descriptor digest | the store's manifests | on change |
| capabilities: per runtime install, the shapes and roles it supports | the runtime's `Shapes()` over the install's facts | on install change |
| throughput: per device, learned streaming bandwidth, compute, and fixed cost, with sample counts | the learning table | on every sample |
| links: to every other member | the link prober | every 5 minutes and on join |
| formations conducted here, seats hosted here, slots here, routes here | the managers | on change |

The mesh profile is the union. `nebu mesh status` prints it. The Mesh page draws it. Every planner input that reads a host profile today can read the mesh profile: a pool gains a node id, a device gains a node id, and the estimate solver's pool list is the same message with more rows.

## Links

The link between two nodes is measured, never assumed from hardware names. On join and every 5 minutes each node measures every other member:

| Measure | How | Cost |
| --- | --- | --- |
| round trip | 20 HTTP/2 ping frames on the session's connection, median and 95th percentile. A ping frame is a round trip of the wire and the kernel, without a request behind it | 20 frames |
| stream bandwidth | one connection, 64 MiB each way, 8 MiB when the last run measured under 100 Mb/s | about 1 s |
| aggregate bandwidth | four connections at once, the same bytes | about 1 s |
| interface | the local interface the route to the peer leaves through: its reported speed, MTU, and the RDMA device bound to it when one is | one read of the system |
| hops | whether the peer is on the interface's own subnet | none |

Both bandwidths matter because runtimes differ: one TCP connection per stage is what llama.cpp gets, and one Arm core drives a single stream at 12 Gb/s on a 200 Gb/s link, while a collective library opens many connections or takes the RDMA device and gets the aggregate. Both directions are measured because asymmetric links exist. The bandwidth runs are skipped while a formation on either node is serving, and the last values stay.

Measurements sort a link into a class the planner and the UI use:

| Class | Requires | Carries well |
| --- | --- | --- |
| fabric | an RDMA device bound on both ends, round trip under 100 µs, aggregate 100 Gb/s or more | lockstep: a reduction per layer per token |
| fast | round trip under 300 µs, stream 10 Gb/s or more | relay: a prompt's cache in a second, chain with long prompts |
| lan | round trip under 2 ms, stream 1 Gb/s or more | chain, draft: one small message per token per seat |
| slow | anything else | replicas: whole requests only |

The classes are cutoffs on the cost model, not the model itself. The planner computes with the measured numbers. The classes exist so a person reading the Mesh page knows what a link is good for, and so a link with a 200 Gb/s interface and no RDMA device on one end, which is what a socket only adapter on one side produces, shows as fast and not fabric.

For lockstep the planner needs a collective's latency, which no ping measures. A fabric link is priced at 45 µs per reduction, the figure measured for small reductions on direct RDMA links, and a socket link at twice its round trip. Both are corrected by the ratio table once a lockstep formation has run on the link.

## Planner

The planner takes a run request, the mesh profile, the descriptor, the runtimes' shape support, and the span, and answers with a formation plan: the shape, the seats, per seat the tensor groups and their pools, and the predicted time to first token and tokens per second at a reference prompt length. It answers the way the estimate answers today, with a verdict and a detail line, and it keeps every shape it rejected with the reason, so the UI can say "lockstep needs a fabric link and this one is lan" beside the shape it chose.

### Inputs

From the descriptor and the attention family, all computed today:

| Symbol | Meaning | Source |
| --- | --- | --- |
| B_g | bytes of tensor group g | `TensorGroup.bytes` |
| N_g | parameters of tensor group g | `TensorGroup.elements` |
| L | layer count | `Params.Layers` |
| d | hidden size | `Params.Embedding` |
| k | cache bytes per token | the family's `CachePerToken` times the cache type's bytes |
| E | share of expert bytes a token reads | `Params.ExpertsUsed / Params.Experts`, 1 for dense models |
| P | reference prompt length | the route's median prompt tokens from traces, else 2048 |

From each node's throughput record:

| Symbol | Meaning |
| --- | --- |
| β_n | bytes per second the device streams weights at during decode |
| γ_n | floating point operations per second during prefill |
| c_n | fixed cost per forward pass, the kernel launch and scheduling floor |

From each link:

| Symbol | Meaning |
| --- | --- |
| r_ab | round trip between a and b |
| w_ab | bytes per second between a and b, stream or aggregate as the runtime's transport takes |
| a_ab | latency of one reduction between a and b |

### Costs

Every shape is an assignment of tensor groups to seats and a rule for what crosses links. The planner prices each candidate with two numbers, then scores them with the route's profile. Activations are 4 bytes per hidden unit, the width llama.cpp moves, and 2 for runtimes that move half precision, a fact each runtime declares.

Decode, one token, one sequence:

```
solo         T = (B + K) / β + c
chain, star  T = Σ_seats ((B_s + K_s) / β_s + c_s) + Σ_stages (r_head,s + 2 × 4d / w_head,s)
chain, ring  T = Σ_seats ((B_s + K_s) / β_s + c_s) + Σ_boundaries (r_b + 2d / w_b)
lockstep     T = max_seats ((B_s + K_s) / β_s + c_s) + 2L × a_worst
relay        T = the decode seat's solo cost
replicas     T = the seat's solo cost
draft        T = (γ × (r + draft step) + target step) / ((1 − α^(γ+1)) / (1 − α))
```

`B_s` is the bytes a seat reads per token: its non expert groups whole and its expert groups at `E` of their bytes, because a token reads only the experts it routes to. `K_s` is the cache the seat holds at the planned context. `a_worst` is the slowest reduction in the lockstep group. `γ` is the draft tokens per round and `α` the acceptance per draft token.

Prefill, P tokens:

```
solo         T = 2 × N × P / γ + c
chain, star  T = Σ_seats (2 × N_s × P / γ_s + c_s) + Σ_stages (r_head,s + 2 × 4d × P / w_head,s)
chain, ring  T = Σ_seats (2 × N_s × P / γ_s + c_s) + Σ_boundaries (r_b + 2d × P / w_b)
lockstep     T = max_seats (2 × N_s × P / γ_s + c_s) + 2L × (a_worst + 2d × P / w_worst)
relay        T = prefill seat's solo cost + max(0, k × P / w_pd − overlap × prefill seat's solo cost)
```

`overlap` is 1 when the runtime streams the cache layer by layer while later layers compute and 0 when it sends the cache after the last layer. A chain runtime that pipelines prompt chunks divides its boundary term by the chunks in flight, a fact the runtime declares.

The relay line says when relay helps: when the prefill seat's compute saves more than the cache costs to move. For a cache of 320 KiB per token a 4096 token prompt moves 1.25 GiB, one second at 10 Gb/s and ten at 1 Gb/s, so relay is a fast link shape, and the measured 2.8 times on a 10 Gb/s link came from an 8k prompt between a node with four times the compute and a node with three times the bandwidth.

The chain lines say why long prompts want short chains on fast links: every boundary moves the whole prompt's activations, and an 8192 wide model at 8192 tokens moves 256 MiB per boundary, two seconds at 1 Gb/s and 20 ms at 100 Gb/s.

The lockstep line says why lockstep wants a fabric: 2L reductions per token, and at 80 layers and 200 µs each that is 32 ms per token before any compute, more than a single device spends decoding a 7B model.

### Search

Candidates come from the shapes every runtime installed on the span's nodes supports. For each shape:

- chain: seats are the span's nodes with a device pool. The planner orders them by β descending and fills each with whole layers to its capacity under the runtime's policy, cache following layers as it does today, embedding and output where the runtime puts them, then tries every subset of the order up to eight nodes and every choice of head among them. The head is the seat holding the last layers and the output, chosen for its links to the stages under the runtime's star or ring cost. Every candidate whose stages hold nothing is dropped.
- lockstep: seats are the span's nodes whose device vendor and device count match, on links of class fabric. Every seat holds every layer divided by the seat count. Seat counts that leave any seat over capacity are rejected with the shortfall.
- relay: one prefill seat, one decode seat, each holding the whole model. Every ordered pair of nodes that fit is a candidate, scored against solo on the better node.
- replicas: every node that fits solo is a seat. Throughput sums. The plan carries the affinity policy the gateway applies.
- draft: the head is a node that fits the target solo, the stage a node that fits the draft model, on any lan link. The draft model is a stored companion of the target, resolved the way the speculative decoding params resolve companions today.
- stages: the denoiser goes to the node with the fastest device that fits it, the encoders and decoder to a node that fits them in host or device memory. Every assignment is a candidate. Costs are per request: encoder parameters times prompt tokens over γ of its seat, denoiser parameters times steps times latent tokens over γ of its seat, decoder parameters times the latent positions it decodes from, the pixels over the VAE's 8 by 8 patch, over γ of its seat, plus the two crossings.

The score is `w_ttft × prefill + w_tps × completion × decode + w_rps / throughput` with weights from the route's profile:

| Profile | Meaning | Weights |
| --- | --- | --- |
| chat | short prompts, short answers | balanced at P = 2048, 256 completion tokens |
| agent | long prompts, short answers | prefill weighted at the route's median prompt |
| batch | many concurrent requests | throughput weighted |
| auto | from the route's last 200 traces: median prompt, median completion, peak concurrency | learned |

The plan chosen is the best score among candidates whose verdict fits. A candidate with verdict partial is kept only when no candidate fits. The plan carries every candidate with its score and reason, sorted, so `nebu inspect --mesh` and the Mesh page show the table.

Devices with no throughput record yet plan with the device profile table's declared numbers, and the plan's detail says so. After one run the learned numbers take over.

## Weights

Seats need files. What each seat needs depends on the runtime, so each role declares what it reads and the conductor's formation task pulls it there first:

| Runtime and role | Needs on its node |
| --- | --- |
| llama.cpp head | the GGUF and its projector, as today |
| llama.cpp stage, draft stage | nothing. The head streams tensors to it at start and it caches them on disk by content hash under its cache directory, so the next start moves only what changed |
| vLLM and SGLang, every rank | the whole checkpoint directory |
| stable-diffusion.cpp head | every part the blueprint fills |
| stable-diffusion.cpp denoiser stage | nothing, as a llama.cpp stage |

Pulling between members is a source. A member's store answers `/blobs/<digest>` on its API listener under the mesh session credential, streaming the blob with range support in the same handler shape as `/files`. A pull on any member checks the mesh before the internet: the puller lists members holding the manifest, picks the one with the best link, and lands each blob from there with the existing fetcher, its workers, chunks, resume, and digest verification unchanged. The transfer schedule and windows apply to internet sources only. The manifest is copied with the blobs and the descriptor with it, so the receiving member's planner sees the model the moment the pull ends.

`nebu pull --to <node>` and the Models page's node picker pull onto another member. A formation task pulls onto every seat that lacks its files and shows one progress row per seat. A tensor cache on a stage counts toward that node's store limit and is collected with the blobs.

## Lifecycle

A formation is a record the conductor writes and every member copies:

| Field | Meaning |
| --- | --- |
| id, name | The route name is the formation name, as an instance's is |
| conductor | The node that owns it |
| shape | One of the shapes |
| seats | node, role, rank, the instance id of the seat's process on that node, its state, its error |
| plan | The formation plan chosen, with the candidates rejected |
| request | The run request, replayed on relaunch |
| state | starting, ready, degraded, stopping, stopped, failed |
| task | The conductor's task, one progress step per seat |

Every seat is an instance on its own node, with the same record, log, triage, measurements, and supervision an instance has today. The run request of a seat carries a seat block: formation id, role, rank, the rendezvous address, and the peers it talks to with their addresses and ports. The runtime's launch sees the seat block and renders the role's command and the role's health check.

Launch order is by role phase, declared by the runtime. Stages first, in parallel, each waited to ready. Then the head, which connects to them. Ranks that rendezvous, as lockstep and ring chain ranks do, launch in parallel in one phase with the same rendezvous address and the conductor waits for the head's health. A seat that fails to reach ready fails the formation. The conductor stops every seat it started, and the formation record keeps every seat's error and triage, so the Formation page shows which node failed and why.

Ready is the head's health, then the head's template probe, then the route. Measurements come from every seat: each seat's runtime parses its own log and the conductor's record sums them by node. The transport a seat negotiated, RDMA or sockets, is a measurement too.

While serving, the conductor watches every seat through the sync. A seat's instance leaving ready puts the formation in degraded and the route in draining, and the conductor stops the rest, because every runtime surveyed takes the group down when one member dies. A conductor missing for three syncs makes every other member mark the formation unreachable and drop its route from their gateways. When the conductor returns, its record wins again.

`desired_running` on a formation relaunches it after a failure or a daemon restart, as instances relaunch today. Relaunch plans again with the mesh as it is: a seat's node that is gone drops out of the span and the planner picks a shape that fits what remains, or fails with the detail. Rendezvous ranks relaunch with the same ranks, addresses, and ports, because the collectives rendezvous by rank.

A daemon restart on a seat's node adopts the seat's process by pid, as instances adopt today. A daemon restart on the conductor adopts the head and re-verifies every seat over the sync before marking the formation ready again.

Stop is the reverse of launch: the head first, so no stage sees a connection drop mid request, then the stages in parallel. Slots swap formations the way they swap instances: a mesh slot's blue green swap launches the new formation beside the old when the mesh fits both, else it drains first.

## Gateway

Routes gain a node id. A route conducted elsewhere has the conductor's gateway as its endpoint and forwarded as its state detail. Every member's gateway lists mesh routes in `/v1/models`, and a request for a forwarded route goes to the conductor's gateway under the mesh session credential, unchanged, so the conductor translates protocols and shapes system messages once and its traces are the record. The entry node records a trace too, marked forwarded, with the conductor's trace id, so the Requests page on either node finds the request.

Replicas are one route with several ready seats. The gateway picks a seat per request:

1. Sessions first. A request whose opening messages hash to a prefix the gateway sent to a seat within the last 10 minutes goes to that seat, so the seat's prefix cache answers. The hash covers the system message and the first user message.
2. Then least loaded. Among seats under their in flight cap, the one with the fewest requests in flight, ties to the best link from the entry node.
3. A seat that refuses or fails passes the request to the next, once, when nothing was streamed yet.

Relay is the gateway too. A relay formation's route knows both seats. The gateway sends the request to the prefill seat for one token with the handoff parameters the runtime's relay support defines, then sends the request to the decode seat with what came back and streams that answer. The two calls are one trace naming both seats. A prompt shorter than the plan's break even length skips the prefill seat and goes to the decode seat alone, because relay costs time to first token on short prompts, and the trace says which path it took. When the prefill seat fails, the request goes to the decode seat alone and the trace says so.

## Runtimes

Every runtime gains three methods:

```go
// Shapes the install supports, from its facts: the binaries it ships, the flags it accepts, the modules it imports.
Shapes(in *v1.Install) []v1.Shape
// Roles of a shape in launch order: phases launch one after another, seats within a phase together.
Roles(shape v1.Shape) []Role
// Renders one seat. The launch carries the seat block: role, rank, rendezvous address, peers with addresses and ports.
LaunchSeat(in Launch) (*Command, error)
```

```go
type Role struct {
	Name  string
	Phase int
	// What the seat's node must hold: weights, parts, or nothing
	Files FileNeed
	// How the seat is known ready: an HTTP path, a hello exchange, or the process alone
	Health Health
	// Whether other seats connect to it, so it gets a guard listener
	Listens bool
}
```

The estimate policy gains per shape cost facts: star or ring, activation bytes per hidden unit, prompt chunks in flight, relay overlap. The planner reads them. Nothing else in `pkg/estimate` changes.

### llama.cpp

| Shape | Roles | Command | Files | Health | Link |
| --- | --- | --- | --- | --- | --- |
| chain | stage, phase 1; head, phase 2 | stage: `ggml-rpc-server -H 127.0.0.1 -p <port> -c -d <devices> -t <threads>` with `LLAMA_CACHE=<cache dir>/rpc/<formation>`. Head: `llama-server --model <gguf> --rpc <stage addresses> --device RPC0,RPC1,…,<local devices> --tensor-split <planned bytes per device> -ngl <all> -fit off` and the flags it takes today | head: the GGUF and its projector. Stage: nothing | stage: the process alive and one hello exchange before the head launches. Head: `/health` | lan |
| draft | stage, phase 1; head, phase 2 | stage: as a chain stage. Head: the solo command with `--spec-draft-model <draft gguf> --spec-draft-device RPC0 --rpc <stage address>` | head: the target, its projector, and the draft GGUF. Stage: nothing | as chain | lan |
| relay | prefill, decode, phase 1 | both: the solo command with `--slot-save-path <cache dir>/slots/<formation>` | both: the GGUF and its projector | each `/health` | fast |
| replicas | replica | the solo command | the GGUF and its projector | each `/health` | any |

The install's facts say what is on: `rpc` is the path of `ggml-rpc-server` beside `llama-server`, `rpc_flag` says `llama-server --help` lists `--rpc`, `draft_device` says it lists `--spec-draft-device`, `slot_save` says it lists `--slot-save-path`. The source recipe sets `GGML_RPC=ON`. The release rule records the facts from the archive's contents. `Shapes()` returns chain when the first two hold, draft when the first three hold, relay when the last holds, replicas always. The protocol between server and head is versioned and the head refuses a stage whose protocol differs, so the planner does not gate on install version: it notes seats on different builds in the plan's sources and lets the head decide at connect.

How chain runs on this runtime, and what the planner prices:

- The head maps the file and streams every tensor to the stage that owns it at start. With `-c` the stage keeps tensors over 10 MiB on disk under its cache directory keyed by content hash, and the next start sends hashes first and skips what the stage has. The first start moves the stage's share of the model across the link once, and `nebu formations show` reports the bytes moved.
- Layers go to devices by `--tensor-split`, proportions over the device list in the order `--device` gives, cumulative from the first layer. The last device in the list takes the last layers and the output. The planner emits the list with the stages first and the head's own devices last, so the output projection and sampling never cross a link, and it emits `--tensor-split` as the planned bytes per device rather than leaving the runtime to split by free memory, because the planner's split follows bandwidth.
- The cache of a layer lives on the device holding the layer, so a stage's capacity counts its layers' cache at the planned context.
- Per token, the head sends the residual to each stage and reads it back. Nothing goes stage to stage. The chain is a star from the head: the links that matter are head to stage, each used twice per token, one blocking round trip per stage per token. The head seat is chosen for its links to the stages and its bandwidth for the last layers. The steady state graph is sent once and replayed by a four byte command per token afterwards, so graph traffic is not priced.
- The transport upgrades itself: two nodes whose link carries an RDMA device on both ends negotiate it at hello and take sockets otherwise. The stage's log says which and the Formation page shows it. Measured on one direct RDMA link, a stage's layers ran at the speed of local layers.
- A stage serves one client. Each stage seat is its own process for one formation, and its guard listener accepts exactly one connection, from the head's node, and refuses every other, so no probe or stray client sits in the server's backlog. The readiness hello is made before the head launches and closed before the head connects.
- A stage dying aborts the head. The formation fails as one thing, every seat is stopped, and the relaunch goes through the planner again with the mesh as it is.
- At most 16 stages. `-dio` is set when a stage's share is over 100 GiB on a unified memory device, and `GGML_CUDA_DISABLE_GRAPHS=1` on a stage whose driver leaks compute graphs under the server, both as triage rules with fixes, the way runtime failures carry fixes today.

Relay on this runtime is the slot file. The gateway sends the request to the prefill seat for one token with a slot id, asks the slot to save, moves the saved state to the decode seat over the mesh blob channel, asks the decode seat's slot to restore it, and sends the request to the decode seat with prompt caching on, so the decode seat continues from the restored cache. The state file is the cache, `k × P` bytes, written on the prefill seat's disk and read on the decode seat's. The planner's relay line for this runtime uses overlap 0 and adds both disks' throughput, and both seats must run the same weights and cache types, which the planner checks from the plan. Builds may differ, the plan notes when they do, and the restore fails on its own when the slot format does not match.

### vLLM

| Shape | Roles | Command, rank n of N | Files | Health | Link |
| --- | --- | --- | --- | --- | --- |
| chain | ranks, one phase | `vllm serve <dir> --pipeline-parallel-size N --tensor-parallel-size <devices on the node> --nnodes N --node-rank n --master-addr <head mesh address> --master-port <p> --distributed-executor-backend mp`, `--headless` on every rank but 0 | whole checkpoint on every rank | rank 0 `/health` | lan |
| lockstep | ranks, one phase | as chain with `--tensor-parallel-size <devices across all nodes> --pipeline-parallel-size 1` | whole checkpoint on every rank | rank 0 `/health` | fabric |
| relay | prefill, decode, one phase | prefill: `vllm serve <dir> --kv-transfer-config '{"kv_connector":"NixlConnector","kv_role":"kv_producer"}'` with `VLLM_NIXL_SIDE_CHANNEL_HOST=<mesh address> VLLM_NIXL_SIDE_CHANNEL_PORT=<p>`. Decode: the same with `kv_consumer` | whole checkpoint on both | each `/health`, then one canary request through the pair | fast |
| replicas | replica | the solo command on each seat | whole checkpoint on each | each `/health` | any |

Ranks launch together with the same `--master-addr` and `--master-port`, chosen by the conductor from its mesh address and a free port. Every rank sets `VLLM_HOST_IP` to its own mesh address, `NCCL_SOCKET_IFNAME` and `GLOO_SOCKET_IFNAME` to the interface the link probe found, and on a link with RDMA devices `NCCL_NET=IB`, `NCCL_IB_HCA` naming them, `NCCL_IB_MERGE_NICS=1` when a link has two, and `NCCL_NET_PLUGIN=none`, so the collective library takes the fabric and not sockets. The launch log line naming `NET/IB` or `NET/Socket` becomes a measurement, so the Formation page says which transport the ranks got. `TORCH_NCCL_HEARTBEAT_TIMEOUT_SEC` is set from the formation's stop grace and `TORCH_NCCL_ENABLE_MONITORING=0` on ranks, because the default monitor aborts an idle rank after ten minutes.

Relay through the gateway: the request goes to the prefill seat with one token to generate and the connector's handoff parameters, the answer carries the handoff, and the same request goes to the decode seat with it. The connector streams the cache over the RDMA device when the link has one and over sockets otherwise, and the measured difference is large enough that the planner prices socket relay with the stream bandwidth and a fabric relay with the aggregate.

`Shapes()` returns chain and lockstep when the install's version accepts `--nnodes`, relay when the NIXL connector imports in the install's environment, replicas always. Lockstep and chain candidates require every rank to have the same vendor and the same device count, because the ranks are symmetric. Install versions may differ and the plan notes it. Speculative settings on the head are off in chain, which this runtime does not compose with pipeline parallel, and the planner's chain candidate says so when the request asked for them.

### SGLang

| Shape | Roles | Command, rank n of N | Files | Health | Link |
| --- | --- | --- | --- | --- | --- |
| chain | ranks, one phase | `python -m sglang.launch_server --model-path <dir> --pp-size N --tp-size <devices on the node> --nnodes N --node-rank n --dist-init-addr <head mesh address>:<p>` with `--chunked-prefill-size` and `--enable-dynamic-chunking`, so the pipeline overlaps prompt chunks | whole checkpoint on every rank | rank 0 `/health` | lan |
| lockstep | ranks, one phase | as chain with `--tp-size <devices across all nodes> --pp-size 1` | whole checkpoint on every rank | rank 0 `/health` | fabric |
| relay | prefill, decode, one phase | prefill: `--disaggregation-mode prefill --disaggregation-transfer-backend nixl --disaggregation-bootstrap-port <p>`. Decode: `--disaggregation-mode decode --disaggregation-transfer-backend nixl`. `--disaggregation-ib-device <dev>` on both when the link has one | whole checkpoint on both | each `/health`, then one canary request through the pair | fast |
| replicas | replica | the solo command on each seat | whole checkpoint on each | each `/health` | any |

The relay protocol of this runtime has the client hand the decode seat the prefill seat's bootstrap address and a room id per request. The gateway does what the runtime's own router does: it adds the bootstrap host, port, and a fresh room id to the request, sends it to both seats at once, and streams the decode seat's answer. `--watchdog-timeout` is set from the stop grace so a hung rank dies instead of answering errors. The chain shape on this runtime pipelines prompt chunks, and its policy declares the chunks in flight, so a ring chain's prefill cost divides by them.

Both engines treat a rank group as one thing: a rank that dies takes the group down, and the conductor stops every rank and relaunches them together with the same ranks, addresses, and ports.

### stable-diffusion.cpp

| Shape | Roles | Command | Files | Health | Link |
| --- | --- | --- | --- | --- | --- |
| stages | denoiser, phase 1; head, phase 2 | denoiser: `ggml-rpc-server -H 127.0.0.1 -p <port> -c -d <devices>`. Head: the solo command with `--rpc-servers <denoiser address> --backend RPC0 --clip-on-cpu --vae-on-cpu` | head: every part the blueprint fills. Denoiser: nothing | denoiser: the process alive and one hello. Head: `/health` | lan |
| replicas | replica | the solo command | every part | each `/health` | any |

Stages on this runtime puts the denoiser on the node with the fastest device and keeps the text encoders and the decoder on the head in host memory. The embedding crosses once per request, the latent crosses once per request, and nothing crosses per step. The denoiser's weights stream to the stage at start and are cached on disk as chain stages cache theirs. The planner's stages cost is per request: encoder parameters times prompt tokens over the head's host compute, denoiser parameters times steps times latent tokens over the stage's device compute, decoder parameters times the latent positions it decodes from over the head's host compute, plus the two crossings. `Shapes()` returns stages when the install's facts record `ggml-rpc-server` and the `--rpc-servers` flag, replicas always.

Replicas of an image or video route spread jobs across nodes through the gateway's media path, which holds one job per seat as it holds one per instance today, so a batch of seeds finishes in the time of the slowest seat's share.

## Learning

The planner's node numbers come from the traces the gateway already records, joined with the plan of the instance that served them:

| Number | From |
| --- | --- |
| β, weight streaming bandwidth | (device bytes of the plan + cache bytes at the trace's context) ÷ time per completion token, from traces with 16 or more completion tokens, solo instances only |
| γ, prefill compute | 2 × parameters × prompt tokens ÷ time to first token, from traces with 256 or more prompt tokens, solo instances only |
| c, fixed cost per pass | the intercept of time per token against bytes per token across the node's traces, from 20 samples on, else 2 ms |
| acceptance, draft tokens accepted per offered | the head's speculative counters at the end of each request, draft formations and solo speculative instances |

Each is an exponentially weighted mean per node and device with its sample count, kept in a `throughput` table beside the calibrations and carried in the node record. Formation traces feed a second table keyed by shape, runtime, and link class: the ratio of predicted to measured time to first token and time per token. The planner multiplies its prediction by that ratio, so a runtime that passes activations more slowly than the model assumes is corrected after a few runs, the way the memory estimate is corrected today.

Nothing is measured by a benchmark step. The first plan on a fresh node uses the device profile table: declared streaming bandwidth and compute by device name pattern, shipped with nebu and extended by `nebu mesh profile set`. The plan's detail names the source of every number it used.

## Security

- The secret crosses the network once per member, inside the join token that admission delivers to a node that asked, over the TLS the member serves when it serves any. After that the handshake proves both sides hold it. Session tokens are what travel, and they expire.
- TLS on the API listener covers node traffic. The join token pins the bootstrap member's certificate fingerprint, and a first contact by address pins the certificate it found. Making a mesh with TLS on makes a mesh certificate authority and every join issues the joining node a certificate, so members verify each other and no fingerprint is pinned after the first contact. Without TLS the daemon warns at join, as it warns about public listeners today.
- Seat processes listen on loopback, as instances do today. A seat that other seats connect to gets a guard listener on the mesh address: a TCP forwarder that accepts connections from the member addresses the role names and forwards to loopback, one connection when the role says so. The forwarder is a kernel splice on Linux, one extra hop of a few microseconds. `mesh.exposure: direct` binds the seat process to the mesh address itself for links where every microsecond counts, and the Mesh page shows which seats are exposed. RDMA transports connect by the link's own address and bypass the forwarder, so a seat that negotiates RDMA is exposed on that link and the Mesh page says so.
- Blob and file serving between members needs the session credential. The web session and API tokens do not open them.
- A node leaving takes its session tokens with it, stops the formations it conducts while the members can still be reached, and drops every formation record it holds, so a mesh made later starts without them. The members it tells drop it at once, and a sync of its still in flight when its goodbye lands does not bring it back: a member enters by handshake alone. A member that is gone is forgotten with `nebu mesh forget <node>` on any member: its record, session, links, and formation copies go there and on every reachable member, and gossip naming it is refused for ten minutes so a member told late does not bring it back. A member that is up and holds the secret returns on its next sync; rotating the secret keeps it out. `nebu mesh reset` leaves any mesh and clears every trace of it, members and formations included, and works outside a mesh too, for what an older daemon left behind. Rotating the secret is `nebu mesh rotate` on any member: it issues a new secret to every reachable member over the sessions, and members unreachable at rotation must join again.

## API

`proto/nebu/v1/mesh.proto`:

```proto
message Node {
  string id = 1;
  string name = 2;
  repeated string addresses = 3;
  string version = 4;
  HostProfile profile = 5;
  repeated Install installs = 6;
  repeated StoredModel stored = 7;
  repeated Capability capabilities = 8;
  repeated Throughput throughput = 9;
  repeated Link links = 10;
  NodeState state = 11;
  uint64 sequence = 12;
  google.protobuf.Timestamp seen_at = 13;
  bool self = 14;
}

message Link {
  string from = 1;
  string to = 2;
  uint32 rtt_us = 3;
  uint32 rtt_p95_us = 4;
  uint64 stream_bytes_per_second = 5;
  uint64 aggregate_bytes_per_second = 6;
  string interface = 7;
  uint64 interface_bits_per_second = 8;
  string rdma_device = 9;
  LinkClass class = 10;
  google.protobuf.Timestamp measured_at = 11;
}

message Throughput {
  string device_id = 1;
  double stream_bytes_per_second = 2;
  double compute_flops = 3;
  double fixed_seconds = 4;
  double acceptance = 5;
  uint32 samples = 6;
  string source = 7;
}

message Capability {
  string runtime_id = 1;
  string install_id = 2;
  repeated Shape shapes = 3;
}

enum Shape { SHAPE_UNSPECIFIED = 0; SHAPE_SOLO = 1; SHAPE_CHAIN = 2; SHAPE_LOCKSTEP = 3; SHAPE_RELAY = 4; SHAPE_REPLICAS = 5; SHAPE_DRAFT = 6; SHAPE_STAGES = 7; }

message Seat {
  string node_id = 1;
  string role = 2;
  uint32 rank = 3;
  string instance_id = 4;
  InstanceState state = 5;
  string error = 6;
  repeated GroupPlacement placements = 7;
  string endpoint = 8;
  string transport = 9;
  // The seat's triage hits and the measurements its runtime parsed, kept on the record
  repeated TriageHit triage = 26;
  repeated Measurement measurements = 27;
}

message FormationPlan {
  Shape shape = 1;
  repeated Seat seats = 2;
  MemoryPlan memory = 3;
  double prefill_seconds = 4;
  double decode_seconds_per_token = 5;
  double requests_per_second = 6;
  uint32 reference_prompt = 7;
  uint32 relay_break_even_prompt = 8;
  repeated Candidate candidates = 9;
  string detail = 10;
}

message Candidate {
  Shape shape = 1;
  repeated string node_ids = 2;
  FitVerdict verdict = 3;
  double score = 4;
  double prefill_seconds = 5;
  double decode_seconds_per_token = 6;
  string reason = 7;
}

message Formation {
  string id = 1;
  string name = 2;
  string conductor = 3;
  Shape shape = 4;
  repeated Seat seats = 5;
  FormationPlan plan = 6;
  RunRequest request = 7;
  FormationState state = 8;
  string error = 9;
  string task_id = 10;
  string slot_id = 11;
  bool desired_running = 12;
  google.protobuf.Timestamp created_at = 13;
  google.protobuf.Timestamp ready_at = 14;
  google.protobuf.Timestamp stopped_at = 15;
  uint64 sequence = 16;
  // Every seat's measurements summed by node, the rendezvous ranks relaunch with, and the key
  // stages keep the model's tensor cache under across relaunches
  repeated NodeMeasurements measurements = 25;
  string rendezvous = 26;
  string cache_key = 27;
}

message NearbyNode {
  string id = 1;
  string name = 2;
  string address = 3;
  bool tls = 4;
  string tls_fingerprint = 5;
  string version = 6;
  string os = 7;
  string arch = 8;
  string mesh_hash = 9;
  string mesh_name = 10;
  uint32 mesh_members = 11;
  bool mesh_tls = 12;
  google.protobuf.Timestamp heard_at = 13;
  bool member = 14;
  string from = 15;
}

message Admission {
  string id = 1;
  AdmissionSide side = 2;
  AdmissionState state = 3;
  NearbyNode node = 4;
  string mesh_hash = 5;
  string mesh_name = 6;
  uint32 mesh_members = 7;
  bool mesh_tls = 8;
  bool invited = 9;
  bool asked = 10;
  string by = 11;
  string detail = 12;
  google.protobuf.Timestamp created_at = 13;
  google.protobuf.Timestamp updated_at = 14;
  google.protobuf.Timestamp expires_at = 15;
  bool reached = 16;
}

message MeshStatus {
  Mesh mesh = 1;
  string node_id = 2;
  string node_name = 3;
  string listen = 4;
  string address = 5;
  bool announce = 6;
  repeated NearbyNode nodes = 7;
  repeated NearbyMesh meshes = 8;
  repeated Admission admissions = 9;
  string listen_error = 10;
}

service MeshService {
  rpc Init(InitMeshRequest) returns (InitMeshResponse);
  rpc Join(JoinMeshRequest) returns (JoinMeshResponse);
  rpc Leave(LeaveMeshRequest) returns (LeaveMeshResponse);
  rpc ForgetNode(ForgetNodeRequest) returns (ForgetNodeResponse);
  rpc ResetMesh(ResetMeshRequest) returns (ResetMeshResponse);
  rpc Rotate(RotateMeshRequest) returns (RotateMeshResponse);
  rpc GetMesh(GetMeshRequest) returns (GetMeshResponse);
  rpc ListNodes(ListNodesRequest) returns (ListNodesResponse);
  rpc Token(TokenRequest) returns (TokenResponse);
  rpc Probe(ProbeLinksRequest) returns (ProbeLinksResponse);
  rpc SetDeviceProfile(SetDeviceProfileRequest) returns (SetDeviceProfileResponse);
  rpc DeleteDeviceProfile(DeleteDeviceProfileRequest) returns (DeleteDeviceProfileResponse);
  rpc Discover(DiscoverRequest) returns (DiscoverResponse);
  rpc Invite(InviteRequest) returns (InviteResponse);
  rpc Ask(AskRequest) returns (AskResponse);
  rpc Admit(AdmitRequest) returns (AdmitResponse);
  rpc Refuse(RefuseRequest) returns (RefuseResponse);
  rpc Dismiss(DismissRequest) returns (DismissResponse);
  // Node to node, without a credential: a node outside holds no session
  rpc Knock(KnockRequest) returns (KnockResponse);
  rpc Offer(OfferRequest) returns (OfferResponse);
  rpc Welcome(WelcomeRequest) returns (WelcomeResponse);
  rpc PlanFormation(PlanFormationRequest) returns (PlanFormationResponse);
  rpc RunFormation(RunFormationRequest) returns (RunFormationResponse);
  rpc ListFormations(ListFormationsRequest) returns (ListFormationsResponse);
  rpc GetFormation(GetFormationRequest) returns (GetFormationResponse);
  rpc StopFormation(StopFormationRequest) returns (StopFormationResponse);
  rpc DeleteFormation(DeleteFormationRequest) returns (DeleteFormationResponse);
  rpc FormationLogs(FormationLogsRequest) returns (stream FormationLogsResponse);
  // Node to node: the handshake admits itself, the rest take the session credential
  rpc Hello(HelloRequest) returns (HelloResponse);
  rpc Sync(SyncRequest) returns (SyncResponse);
  rpc Stream(StreamRequest) returns (stream StreamResponse);
  rpc RunSeat(RunSeatRequest) returns (RunSeatResponse);
  rpc StopSeat(StopSeatRequest) returns (StopSeatResponse);
  rpc GetSeat(GetSeatRequest) returns (GetSeatResponse);
  rpc SeatLogs(SeatLogsRequest) returns (stream SeatLogsResponse);
  rpc PullSeat(PullSeatRequest) returns (PullSeatResponse);
  rpc WatchPull(WatchPullRequest) returns (stream WatchPullResponse);
  rpc GetStored(GetStoredRequest) returns (GetStoredResponse);
  rpc MoveSlot(MoveSlotRequest) returns (MoveSlotResponse);
  rpc DropSlot(DropSlotRequest) returns (DropSlotResponse);
  rpc Rekey(RekeyRequest) returns (RekeyResponse);
  rpc Bye(ByeRequest) returns (ByeResponse);
}
```

`RunRequest` gains `span`, node ids and node qualified device ids, `shape`, auto or one shape, `profile`, what the planner weighs, and `free`, planning against free memory as a swap does. `Instance` gains `seat`. `Route` gains `node_id`, `seats`, and the replicas plan's `affinity`. `Task` progress gains one row per seat for launches and pulls. `Slot.device_ids` accept `node/device`. `Config` gains `mesh { listen, advertise, announce, exposure }`. `EventKind` gains `NODE`, `FORMATION`, and `MESH`, and every member's event stream carries the whole mesh's node and formation events and this node's mesh status, so the UI on any member is live for all of them. `Install.facts` gain the flags and binaries the shapes depend on.

Tables: `mesh_identity`, `mesh` (one row), `mesh_members`, `mesh_admissions`, `mesh_sessions`, `mesh_links`, `throughput`, `formation_ratios`, `device_profiles`, `formations`, `formation_seats`, and `formation_candidates`, generated by Atlas from `schema.sql` as every table is.

Packages: `internal/mesh` for identity, handshake, sessions, sync, discovery, admission, and the member table; `internal/mesh/links` for probes; `internal/formations` for the conductor, seats, and lifecycle; `pkg/estimate/mesh` for the formation planner over the existing solver; `pkg/perf` for the device profile table and the throughput learner; `pkg/runtimes` gains the shape and seat interface every runtime implements.

## CLI and UI

```
nebu mesh init [--name] [--tls]
nebu mesh nearby                    meshes and nodes heard on the network, admissions under way
nebu mesh ask <mesh> [--via node]   ask to join a mesh heard, or accept its invitation
nebu mesh ask --address host:port   ask through a member at an address
nebu mesh invite <node>             invite a node heard, in no mesh
nebu mesh invite --address host:port
nebu mesh admit <node>              admit a node that asked, from any member
nebu mesh refuse <node|mesh>        deny a node that asked, or decline an invitation
nebu mesh dismiss <node|mesh>       clear a settled admission
nebu mesh token                     print a join token, for a node no address reaches
nebu mesh join <token>
nebu mesh leave                     its formations stopped and forgotten too
nebu mesh forget <node> [--here]    forget a member that is gone, on every reachable member
nebu mesh reset                     leave any mesh and clear every trace of it
nebu mesh status                    nodes, links, and formations
nebu mesh nodes
nebu mesh links [--probe]
nebu mesh rotate
nebu mesh profile set <device pattern> --stream <GB/s> --compute <TFLOPS>
nebu inspect <repo> --mesh          the fit table with every shape and node set
nebu run <repo> --span a,b,c --shape auto|chain|lockstep|relay|replicas|draft|stages
nebu formations [list|show|stop|delete|logs]
nebu pull <repo> --to <node>
```

The Mesh page, beside Host in the navigation, is where a mesh is made, joined, and run; nothing about membership needs the CLI. Outside a mesh it lists the meshes heard on the network with Ask to join beside each, the invitations received with Accept and Decline, an address field for a network that passes no beacons, and Make a mesh. Inside one it lists the nodes heard that are in no mesh with Invite beside each, the admissions under way with Admit, Deny, and Dismiss, then a node grid with device meters as the Host page draws them and Forget beside every other node, a link matrix with round trip, bandwidth, and class, the formations with a seat row per node showing its transport and Delete beside each, and the secret's rotation and the mesh's reset. Everything on it is live: a MESH event carries the status whenever a beacon, an admission, or the membership changes. The run dialog gains a span picker and a shape row that shows the planner's candidate table with predicted time to first token and tokens per second, the chosen shape first and every rejected one with its reason. The slot form's device picker lists devices under their nodes. The Models page shows which nodes hold each model and pulls to any of them.

## Walkthroughs

Generic meshes, the numbers the cost model produces for them, and what the published measurements of such pairs report.

**Two identical unified memory boxes, 128 GiB at 273 GB/s each, joined by a direct 200 Gb/s cable with RDMA devices on both ends.** The link probes as fabric: 30 µs round trip, 12 Gb/s per stream, 190 Gb/s aggregate. A 235B mixture of experts at 4 bits is 134 GiB and fits neither box, and a token reads 22B of its parameters, 12.5 GiB. Chain on llama.cpp fits: 60 layers on the head, 34 on the stage, one round trip per token, and the cost model predicts 19 tokens per second before any ratio is learned; measured on such a pair, 12.5, a gap the ratio table closes after the first run. Lockstep on vLLM fits with matching installs: each seat reads 6.25 GiB per token in 23 ms, plus 188 reductions at 45 µs, predicted 28 tokens per second; measured, 21 to 25. The planner picks lockstep for a chat profile and the candidate table shows chain beneath it with "star chain, one round trip per token, stage holds 34 layers at 273 GB/s". On the same pair a 405B dense model at 4 bits, 205 GiB, fits neither box alone; lockstep runs it at under 3 tokens per second with the chain slower beneath it, and the candidate says why: 205 GiB streamed at 546 GB/s combined is the floor.

**A desktop with a 24 GiB device at 900 GB/s, a 128 GiB unified memory box at 256 GB/s, and a laptop with 32 GiB of host memory, on a 2.5 Gb/s switch.** Every link probes as lan: 180 µs round trip, 2.3 Gb/s. A 70B model at 4 bits is 40 GiB. Chain on llama.cpp: the planner orders by bandwidth, the desktop's device first, and fills it with 40 layers and their cache, 22 GiB; the unified box takes the other 40; the laptop holds nothing and is dropped. Two seats, the desktop as head because its link to the box is the only one that matters: 24 ms on the desktop, 84 ms on the box, 4 ms of fixed cost, a round trip, predicted 9 tokens per second. A 2048 token prompt prefills near 9 s, and the boundary's 64 MiB each way at 2.3 Gb/s is half a second of that. Lockstep is rejected: "link class lan, 180 µs, needs fabric". Relay is rejected: "no node fits the model alone". The chat profile picks chain.

**A workstation with a 48 GiB device at 900 GB/s that fits a 70B model at 4 bits with its cache and 2 GiB to spare, and a laptop with a 16 GiB device at 250 GB/s beside it on a 1 Gb/s link.** Solo decodes at 19 tokens per second. Draft: the 1.5B draft model at 8 bits goes to the laptop's device, 5 draft tokens per round, each a 400 µs round trip and an 8 ms draft step, verified in one 55 ms target step, acceptance 0.7 learned from earlier solo speculative runs: 2.9 tokens per 97 ms round, 30 tokens per second, predicted 1.6 times solo. Replicas is rejected: "the laptop does not fit the model". Relay is rejected: "the laptop prefills slower than the head". The chat profile picks draft, and the candidate table shows solo at 19 beneath it.

**A compute rich box, 100 TFLOPS at 273 GB/s, and a bandwidth rich box, 26 TFLOPS at 819 GB/s, on a 10 Gb/s link.** Both fit an 8B model at 16 bits. The link probes as fast: 210 µs, 9.4 Gb/s. For a route whose traces show 8k prompts, relay: prefill on the compute rich box at 1.5 s, cache of 1 GiB moved at 9.4 Gb/s in 0.9 s overlapped layer by layer, decode on the bandwidth rich box at 38 tokens per second: 2.3 s for the request against 4.3 solo on the compute box and 6.4 on the bandwidth box. Measured on such a pair, 2.32 s against 4.34 and 6.42. The plan states the prompt length above which the relay pays, the exact crossing where the pair beats the better box alone, and the gateway sends every request shorter than it to the bandwidth rich box alone.

**A box with a 24 GiB device and 16 GiB of host memory, and a box with a 4 GiB device and 64 GiB of host memory, on a 1 Gb/s link, running a video pipeline whose denoiser is 14 GiB, text encoder 11 GiB, and decoder 1 GiB.** Solo on the first box: 26 GiB of parts against 24 GiB of device and 16 GiB of host memory, with the runtime's overhead and the encoder's activations, verdict no. Solo on the second: a 14 GiB denoiser against a 4 GiB device, verdict no. Stages: the head on the second box with the encoders and decoder in its host memory, the denoiser on the first box's device with 10 GiB left for its activations. At 1 TFLOPS on the head's cores and 80 TFLOPS on the device, for five 1024 by 1024 frames from a 2048 token prompt, the plan prices the request as 24 s of encoding on the head's cores, the 20 MiB embedding across in 168 ms, 50 denoising steps on the device in 192 s, the 640 KiB latent back in 5 ms, and 88 s of decoding on the head's cores, and it is the only candidate that fits.

## Delivery

Each phase is a feature whole on its own. A phase ships when its promise holds on two nodes on one LAN, with its tests.

| Phase | Promise | Touches |
| --- | --- | --- |
| 1 membership | Two nodes on one network see each other on their Mesh pages, one asks or invites and the other decides, they join, see each other's profiles, installs, stored models, and links, and stay synced across restarts. The Mesh page and `nebu mesh` show it | mesh proto, `internal/mesh`, `internal/mesh/links`, config, guard, events, CLI, Mesh page |
| 2 store sharing | A pull on one node lands from another node holding the model, and `--to` pulls onto another node | store handler, puller source, Models page |
| 3 chain and draft | A GGUF too large for either node runs across both on llama.cpp, and a draft model on the second node speeds up a model that fits the first, both planned by the formation planner, routed on every gateway, learned from traces | seat runtime interface, llama.cpp shapes and install facts, `internal/formations`, `pkg/estimate/mesh`, `pkg/perf`, gateway forwarding, run dialog, Formation page |
| 4 replicas and relay | A model that fits one node runs as replicas across nodes with affinity routing, and as a relay on the three runtimes with cache handoff | gateway seat picking, relay proxying, vLLM and SGLang relay roles, llama.cpp slot relay |
| 5 lockstep and ring chain | Matching nodes on a fabric link run tensor parallel on vLLM and SGLang, and pipeline parallel on lan links | vLLM and SGLang rank roles, rendezvous launch, transport measurement |
| 6 stages | A diffusion pipeline runs its denoiser on one node and its encoders and decoder on another | stable-diffusion.cpp stage roles |
| 7 mesh slots | Slots span nodes, swap formations blue green, and relaunch after restarts | slot span, slot swap of formations |
