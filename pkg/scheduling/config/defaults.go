package config

// The profile set for a pool whose runtime lists its adapters, endpoints picked by the gateway's own counts
const DefaultConfig = `plugins:
- type: ready-filter
  name: ready
- type: subset-filter
  name: subset
- type: capacity-filter
  name: capacity
- type: lora-affinity-filter
  name: lora-filter
- type: objective-filter
  name: objective
- type: prefix-cache-scorer
  name: prefix
- type: lora-affinity-scorer
  name: lora
- type: assumed-load-scorer
  name: assumed
- type: latency-scorer
  name: latency
- type: throughput-scorer
  name: tps
- type: max-score-picker
  name: picker
  parameters:
    count: 2
- type: assumed-load
  name: assumed-load
- type: throughput
  name: throughput
- type: single-profile-handler
  name: handler
  parameters:
    profile: default
profiles:
- name: default
  plugins:
  - pluginRef: ready
  - pluginRef: subset
  - pluginRef: capacity
  - pluginRef: lora-filter
  - pluginRef: objective
  - pluginRef: prefix
    weight: 3
  - pluginRef: lora
    weight: 1
  - pluginRef: assumed
    weight: 1
  - pluginRef: latency
    weight: 2
  - pluginRef: tps
    weight: 1
  - pluginRef: picker
profileHandler:
  pluginRef: handler
`

// The profile set for a pool whose runtime exports no scheduling metrics
const DefaultNoMetricsConfig = `plugins:
- type: ready-filter
  name: ready
- type: subset-filter
  name: subset
- type: capacity-filter
  name: capacity
- type: objective-filter
  name: objective
- type: assumed-load-scorer
  name: assumed
- type: prefix-cache-scorer
  name: prefix
- type: latency-scorer
  name: latency
- type: throughput-scorer
  name: tps
- type: max-score-picker
  name: picker
  parameters:
    count: 2
- type: assumed-load
  name: assumed-load
- type: throughput
  name: throughput
- type: single-profile-handler
  name: handler
  parameters:
    profile: default
profiles:
- name: default
  plugins:
  - pluginRef: ready
  - pluginRef: subset
  - pluginRef: capacity
  - pluginRef: objective
  - pluginRef: assumed
    weight: 2
  - pluginRef: prefix
    weight: 3
  - pluginRef: latency
    weight: 2
  - pluginRef: tps
    weight: 1
  - pluginRef: picker
profileHandler:
  pluginRef: handler
`

// The profile set for a disaggregated pool, prefill joining decode once a prompt reaches 256 tokens
const DefaultPDConfig = `plugins:
- type: ready-filter
  name: ready
- type: subset-filter
  name: subset
- type: role-filter
  name: prefill-role
  parameters:
    role: prefill
- type: role-filter
  name: decode-role
  parameters:
    role: decode
- type: capacity-filter
  name: capacity
- type: lora-affinity-filter
  name: lora-filter
- type: prefix-cache-scorer
  name: prefix
- type: lora-affinity-scorer
  name: lora
- type: assumed-load-scorer
  name: assumed
- type: latency-scorer
  name: latency
- type: expert-parallel-scorer
  name: ep
- type: throughput-scorer
  name: tps
- type: max-score-picker
  name: picker
  parameters:
    count: 2
- type: assumed-load
  name: assumed-load
- type: throughput
  name: throughput
- type: pd-profile-handler
  name: handler
  parameters:
    prefillProfile: prefill
    decodeProfile: decode
    threshold: 256
profiles:
- name: prefill
  plugins:
  - pluginRef: ready
  - pluginRef: subset
  - pluginRef: prefill-role
  - pluginRef: capacity
  - pluginRef: lora-filter
  - pluginRef: prefix
    weight: 3
  - pluginRef: lora
    weight: 1
  - pluginRef: assumed
    weight: 1
  - pluginRef: ep
    weight: 1
  - pluginRef: tps
    weight: 1
  - pluginRef: picker
- name: decode
  plugins:
  - pluginRef: ready
  - pluginRef: subset
  - pluginRef: decode-role
  - pluginRef: capacity
  - pluginRef: lora-filter
  - pluginRef: prefix
    weight: 3
  - pluginRef: lora
    weight: 1
  - pluginRef: assumed
    weight: 1
  - pluginRef: latency
    weight: 2
  - pluginRef: ep
    weight: 1
  - pluginRef: tps
    weight: 1
  - pluginRef: picker
profileHandler:
  pluginRef: handler
`

// The profile set for replicas of one model across nodes, prefix cache affinity weighted highest
const DefaultReplicasConfig = `plugins:
- type: ready-filter
  name: ready
- type: subset-filter
  name: subset
- type: capacity-filter
  name: capacity
- type: lora-affinity-filter
  name: lora-filter
- type: prefix-cache-scorer
  name: prefix
- type: assumed-load-scorer
  name: assumed
- type: latency-scorer
  name: latency
- type: expert-parallel-scorer
  name: ep
- type: throughput-scorer
  name: tps
- type: max-score-picker
  name: picker
  parameters:
    count: 2
- type: assumed-load
  name: assumed-load
- type: throughput
  name: throughput
- type: single-profile-handler
  name: handler
  parameters:
    profile: replicas
profiles:
- name: replicas
  plugins:
  - pluginRef: ready
  - pluginRef: subset
  - pluginRef: capacity
  - pluginRef: lora-filter
  - pluginRef: prefix
    weight: 4
  - pluginRef: assumed
    weight: 1
  - pluginRef: latency
    weight: 1
  - pluginRef: ep
    weight: 1
  - pluginRef: tps
    weight: 1
  - pluginRef: picker
profileHandler:
  pluginRef: handler
`
