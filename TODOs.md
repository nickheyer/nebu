# TODOs

Gaps between nebu and the current standard for routing to self-hosted inference. Each item is a behavior nebu must have.

## Endpoint data layer

- [ ] Scrape every running runtime at a fast fixed interval for queued requests, running requests, KV cache utilization, KV block size, total KV blocks, max LoRA, running LoRA adapters, and waiting LoRA adapters
- [ ] Map each runtime's metric names onto one schema so the scheduler reads one shape regardless of runtime (llama.cpp, vLLM, SGLang, NeMo)
- [ ] Turn on each runtime's metrics endpoint at launch and read KV totals from it rather than from log lines
- [ ] Read vLLM max_num_seqs and SGLang max_running_requests into the route's concurrency capacity, which today comes only from llama.cpp n_parallel
- [ ] Run one collector per endpoint with a staleness threshold, so an endpoint whose metrics are stale is filtered out of selection
- [ ] Keep per-endpoint attributes in an extensible map keyed by collector so new data sources add attributes without changing core types
- [ ] Define data source and attribute collector interfaces with a registry, so a new source (a cache index reporter, a model list scraper) is a plugin
- [ ] Keep probing runtime health after READY and drop an endpoint from rotation when it stops answering, instead of only on process exit
- [ ] Make the pool concept explicit: a group of endpoints sharing base model, runtime and accelerator, with a selector, a target port list, and a transport protocol
- [ ] Support several target ports per process, each addressed as its own endpoint, for data-parallel ranks
- [ ] Let an endpoint declare which of its ports are active so inactive ranks leave rotation without the process leaving
- [ ] Resync the whole endpoint set when the pool selector or port list changes, and on startup before the first request is accepted

## Scheduler framework

- [ ] Replace the fixed least-in-flight sort with a pipeline of filters, weighted scorers, and one picker, each a plugin behind a stable interface
- [ ] Normalize every scorer to the range zero to one and apply weights at profile level
- [ ] Support named scheduling profiles, several per request, chosen by a profile handler that sees the request and prior profile results
- [ ] Support a shadow profile that runs and logs but does not route, so a new algorithm is tested on live traffic
- [ ] Give each scheduling cycle its own state object with a lifecycle tied to the request
- [ ] Keep the framework free of runtime and host concepts, with opinions in plugins only
- [ ] Load scheduler profiles, plugin lists and weights from a configuration file, not from compiled constants
- [ ] Let an out-of-process scheduler replace the in-process one per pool, with the in-process one as fallback
- [ ] Decide an endpoint within ten milliseconds on average under load
- [ ] Apply the scheduler to every route shape, not only replicas, so solo routes with several instances of the same model on different nodes are pooled and balanced
- [ ] Pool same-named instances across nodes into one route instead of a local route shadowing a forwarded one

## Scoring signals

- [ ] Score on scraped queue depth, not on the gateway's own in-flight counter
- [ ] Score on KV cache utilization and filter endpoints above a utilization threshold
- [ ] Score on LoRA affinity: prefer endpoints with the adapter loaded, then endpoints with free adapter capacity, and queue when every endpoint is at max adapters
- [ ] Score on predicted TTFT and TPOT per endpoint from an online latency model fed by completed requests
- [ ] Estimate per-request cost from prompt length and expected output length and account for it against endpoint capacity at pick time
- [ ] Weight scores by per-endpoint throughput so a faster accelerator takes more load
- [ ] Track assumed load: count a request against its endpoint at pick time, before any tool connect or image fetch, and release it on stream end
- [ ] Make the pick and the count one atomic step so concurrent requests cannot both pick the same seat past its cap
- [ ] Select among several prefill and several decode seats with the same scorers, instead of the last one of each

## Prefix cache aware routing

- [ ] Build an approximate prefix cache index: split the prompt into fixed-size chunks, hash each chunk with the previous chunk's hash, and record which endpoints hold each chunk hash
- [ ] Choose the endpoint with the longest matching chunk prefix, then weigh that against queue and KV utilization so affinity does not overload a server
- [ ] Scope the index per model and per adapter, since adapters do not share cache
- [ ] Evict index entries with an LRU sized from each endpoint's total KV blocks and block size, mirroring the runtime's own eviction
- [ ] Bound the index memory and make every update constant time, replacing the whole-map scan that runs once the affinity table passes 4096 entries
- [ ] Accept a reported prefix index from a runtime when one is available and merge it with the approximate index
- [ ] Fall back to consistent hashing of the prefix when no index entry matches, so parallel gateways pick the same server
- [ ] Do not pin a prefix to a seat that answered with an error
- [ ] Turn on prefix caching by default in every runtime launch that supports it

## Flow control, priority and fairness

- [ ] Queue requests before dispatch instead of refusing immediately with 429 when a cap is hit
- [ ] Accept a per-request objective by header that names a priority band or integer priority, with a default band for anonymous requests
- [ ] Accept a per-request fairness id by header and share capacity fairly between ids, defaulting the id to the authenticated user or token
- [ ] Shed the lowest priority band first under saturation, answering 429, and shed the highest band last
- [ ] Keep per-flow queues with a dispatch policy and a queue timeout
- [ ] Detect hard saturation from scraped queue and KV metrics, not from static caps that default to unlimited
- [ ] Detect soft saturation: an endpoint whose predicted latency misses the request's objective is unusable for that request
- [ ] Accept per-request latency objectives for TTFT and TPOT and route to endpoints predicted to meet them
- [ ] Enforce caps and rate limits once per request across the mesh, not once at the entry node and again at the conductor
- [ ] Add usage metering per fairness id in tokens and requests, as the basis for fairness and quotas

## Request path

- [ ] Extract the model name with a targeted parse of one field, before and without a full unmarshal of the body
- [ ] Stop the second unmarshal into a raw map at every nesting level for unknown fields
- [ ] Route on headers alone when the model name arrives by header, and defer the body read until the decision is made
- [ ] Cap body size at a routing-appropriate size (tens of megabytes) and process bodies as streamed chunks of at most 62 KB
- [ ] Add weighted traffic splitting between target models or adapters behind one public name, for canary and A/B rollout
- [ ] Add model name rewrite rules with exact match, with precedence to exact over catch-all, then oldest rule, then list order
- [ ] Rewrite the model field in place without re-marshalling and re-ordering the whole body
- [ ] Accept a caller-supplied endpoint subset and pick only from it, answering 503 when none of the subset is eligible
- [ ] Return an ordered list of endpoints (primary plus fallbacks) and retry down the list on connection failure
- [ ] Report the endpoint that actually served each request to the client in a response header and to the scheduler for load accounting
- [ ] Mark every retry decision with whether bytes reached the client, and never resend a non-idempotent request after the upstream started generating
- [ ] Distinguish the two refusal codes strictly: 503 only when no ready endpoint exists, 429 only when the request is shed by policy
- [ ] Make fail-open versus fail-close a configured policy per pool when the scheduler itself errors, defaulting to fail-close
- [ ] Fall back to a plain endpoint choice when the scheduler is unavailable and fail-open is set
- [ ] Record the served endpoint for relays and forwarded requests too, not only for replicas
- [ ] Propagate the request id to the runtime instead of stripping every internal header
- [ ] Speak HTTP/2 cleartext to runtimes that support it, selected by a per-pool transport protocol field defaulting to HTTP/1.1
- [ ] Speak gRPC to vLLM and SGLang backends: gRPC in and out, and HTTP in with gRPC out via transcoding of requests, responses and SSE streams
- [ ] Add tokens-in tokens-out mode so tokenization can run at the gateway
- [ ] Serve the Responses API natively on runtimes that support it instead of always translating to chat completions
- [ ] Pool connections for image fetches instead of building and discarding a transport per request
- [ ] Route request-supplied MCP connections through the same outbound guard as image and web fetches
- [ ] Send keepalive pings on Ollama NDJSON streams
- [ ] Compute queued-behind for replicas against the serving seat's concurrency, not the route-wide counter

## Request processing pipeline

- [ ] Make the processing chain a registry of plugins with factories, configured by file, instead of a compiled slice of three
- [ ] Add a response stage to the chain
- [ ] Support a chain topology that can run stages concurrently as a DAG
- [ ] Parse the body once into a shared structure that every stage reads, and let a stage return a mutated copy
- [ ] Add a guardrail plugin interface and a reference implementation
- [ ] Run the request stage for media requests, for forwarded requests at the entry node, and when a parse fails
- [ ] Apply the system-prompt override and route tool servers on the passthrough path too

## Health and readiness

- [ ] Split liveness from readiness: liveness answers when the process responds, readiness answers only after the endpoint set has synced
- [ ] Serve the gRPC health checking protocol with a service name per subsystem
- [ ] Serve health on every listener the gateway is mounted on, including the API listener when the gateway has its own
- [ ] Gate the gateway listener on readiness instead of binding before recovery finishes
- [ ] Drain in-flight requests on shutdown before cancelling contexts
- [ ] Drain on direct instance stop and on formation degrade, not only on slot retire
- [ ] Move a route out of READY the moment its instance enters STOPPING
- [ ] Keep in-flight counters alive until the instance drains instead of deleting them on route removal

## Declarative resources and status

- [ ] Give every resource a desired spec and an observed status with typed conditions: type, status, reason, message, lastTransitionTime, observedGeneration
- [ ] Define the condition set per resource: Accepted, ResolvedRefs, Ready, Exported, with the allowed reasons per condition
- [ ] Record status per parent and per controller so several gateways or nodes each own their own entry and never overwrite another's
- [ ] Remove a controller's status entry when it stops managing the resource
- [ ] Reconcile continuously toward the desired state for instances and slots, restarting a crashed solo instance, not only at daemon start
- [ ] Persist the full route record: policy, shape, seats, formation id and forwarded flag
- [ ] Keep policy for instance aliases across restart
- [ ] Add alpha and stable API channels with separate packages and a written deprecation policy with a release window
- [ ] Run breaking-change detection on protos in CI
- [ ] Reserve every removed field and enum number
- [ ] Validate every input with declarative rules at the schema layer, including range checks on policy values, and reject negative rates
- [ ] Inherit the default for any unset policy field, including a negative one
- [ ] Add reverse migrations and a tested upgrade path instead of renaming an old database aside
- [ ] Hot reload the config file for gateway keys, default policy and log level

## Multi-node and multi-cluster

- [ ] Export a pool from one mesh and import it into another as a controller-managed resource that carries the exporting node set and parent status
- [ ] Support endpoint mode (route straight to remote endpoints picked by the remote scheduler) and parent mode (route through the remote gateway) as configured routing modes
- [ ] Support hub and spoke as well as push and pull sync topologies for exports
- [ ] Pick between several pools or imports by weight, and by scheduler metrics such as ready endpoint count, with active-passive by health as the minimum
- [ ] Allow more than one hop when a route crosses meshes, with a hop limit
- [ ] Send sync deltas instead of the full node record to every member every five seconds
- [ ] Stop traffic counters from triggering a full mesh sync every second
- [ ] Replace the full-mesh all-to-all sync with a topology that scales past tens of nodes
- [ ] Garbage collect imports when an export is withdrawn and drain traffic before removal
- [ ] Standardize remote scheduler discovery (ports, names) so health and metrics can be found without configuration
- [ ] Bootstrap mutual TLS between an importing gateway and an exported pool

## Disaggregation and parallelism

- [ ] Treat data-parallel ranks as separate endpoints and balance across them
- [ ] Make the scheduler expert-parallel aware when scoring wide expert-parallel seats
- [ ] Generalize the prefill and decode handoff into a plugin per protocol, selected by profile, instead of three hardcoded paths
- [ ] Give the decode seat failover and cap checks like replicas have
- [ ] Move the llama.cpp slot save, move, restore and drop off the request path
- [ ] Degrade only the failed seat of a replicas formation instead of relaunching every seat

## Runtime launch and lifecycle

- [ ] Serve a LoRA adapter named in the model field: resolve the adapter, route to an endpoint with it loaded, and load it dynamically when none has it
- [ ] Load and unload adapters at runtime through each runtime's adapter API
- [ ] Report adapter availability per endpoint from the scraped adapter metrics
- [ ] Pin the install id in saved requests so a relaunch never silently changes runtime version
- [ ] Refuse to remove an install that a slot or formation still references
- [ ] Add canary and percentage traffic shift to swaps, with automated rollback on objective regression
- [ ] Add request-triggered launch and scale-to-zero so a pending route starts its model instead of answering 503
- [ ] Add idle unload driven by scraped load, and use vLLM sleep mode with wake on demand
- [ ] Add process snapshot and restore for fast scale-up
- [ ] Expose context length, quantization, accelerator and throughput class on the route and in the models listing
- [ ] Report the real capabilities of embedding-only launches in the models listing
- [ ] Pin devices for vLLM, SGLang and NeMo launches outside slots so the plan and the runtime agree on the GPU
- [ ] Name ggml devices by their renumbered visible index, not the host-global index
- [ ] Read the port into the runtime atomically instead of bind, close, reuse
- [ ] Replace the 100 ms log tail and pid polls with event-driven reads
- [ ] Do not hold the host probe mutex across the whole probe so launches do not queue behind it
- [ ] Raise the prepare-stage line cap so a progress line without newline cannot stall conversion for hours
- [ ] Kill orphaned workers of an adopted process group once the root is gone
- [ ] Record an exit during draining as a crash when it was one

## Observability

- [ ] Expose a metrics endpoint with per-request latency, TTFT, TPOT, queue wait, tokens in and out, per model, per endpoint, per priority band and per fairness id
- [ ] Expose objective attainment metrics per priority band
- [ ] Expose scheduler metrics: pick latency, filter outcomes, score distributions, flow control queue depth and shed counts
- [ ] Expose aggregate metrics suitable for an external autoscaler: queue depth, utilization and ready endpoint counts per pool
- [ ] Emit tracing spans with propagated context from gateway through scheduler to runtime
- [ ] Persist or export traces instead of an in-memory ring that is lost on restart
- [ ] Scope traces and video jobs per account instead of sharing bodies across all users
- [ ] Record the serving endpoint and the scheduler decision (filters applied, scores) on every trace
- [ ] Log streaming RPC failures, which the unary interceptor misses
- [ ] Exclude tool rounds from TTFT and TPOT learning and from prompt token sums
- [ ] Set first-byte time when the runtime's headers arrive, not when the client header is written

## Security

- [ ] Serve the scheduler and gateway with TLS by default using a generated certificate, with plaintext as the opt-in
- [ ] Use mutual TLS between mesh hosts with client authentication, hostname verification and a revocation path
- [ ] Stop distributing the mesh CA private key to every member
- [ ] Give join tokens an expiry and make them distinct from the raw mesh secret
- [ ] Keep the mesh listener closed until a mesh exists, and warn on its exposure like the other listeners
- [ ] Make first-account setup atomic and not reachable from the mesh listener
- [ ] Put reflection behind the auth guard
- [ ] Hash stored API tokens and redact secrets from settings reads, events and tool server status
- [ ] Add token scopes and roles so a token can be limited to inference
- [ ] Keep env values out of persisted and published instance commands
- [ ] Restrict password changes to the account's owner or an admin role
- [ ] Turn the MCP localhost protection back on

## Testing and conformance (LOW PRIORITY, DO LAST WHEN NOTHING ABOVE OR BELOW IS INCOMPLETED)

- [ ] Write a conformance suite that any gateway or scheduler implementation can run against the routing protocol, with named features and published per-implementation reports per release
- [ ] Add conformance tests for: endpoint served reported, traffic confined to picked endpoints, data-parallel port confinement, weighted split across pools, fail-open when the scheduler is down, invalid pool reference status, missing scheduler reference status, transport protocol defaults
- [ ] Add a model server stub that emulates queueing, KV cache fill and eviction, and adapter loading without an accelerator, with time-accurate and ratio modes
- [ ] Add a benchmark harness that replays recorded production traces against a scheduler profile and reports latency and throughput
- [ ] Add unit tests for every filter, scorer and picker, and integration tests for concurrent and cooperative scheduling
- [ ] Test the token bucket, slot swap, evict, relaunch, recover, alias rename and formation launch, degrade and relaunch
- [ ] Test that a pinned seat wins while busier but under its cap
- [ ] Test the local-after-forwarded route order
- [ ] Add fuzz tests for body parsing and model name extraction
- [ ] Add tests for every package that has none, including the CLI, MCP server and fake instance package

## Efficiency

- [ ] Remove the global route table mutex from the request path; read a lock-free snapshot per request
- [ ] Stop cloning the whole route proto with seats on every request
- [ ] Read seat in-flight counts once per pick, not twice per sort comparison through the lock
- [ ] Cache link distances instead of cloning and sorting every link of every member twice per comparison
- [ ] Publish counter events outside the lock
- [ ] Drain queued route writes on every mutator so records and events are written when they happen
- [ ] Skip re-saving unchanged forwarded routes on every node event
- [ ] Stop writing the member row and publishing a node event on every equal-sequence sync
- [ ] Keep one body copy: stop the trace string copy, the clone at trace start and finish, the failover re-read, and the double decode call for NIXL
- [ ] Stop building the trace body for ghost requests
- [ ] Parse an uploaded multipart body once
- [ ] Stop running a full doctor with network searches on every instance exit
- [ ] Fan out peer calls concurrently
- [ ] Stop the synchronous client write tee that blocks client writes on the trace parser
