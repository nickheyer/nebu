# nebu v0.5.3-0.20261103054159-6598d5f3004d+dirty 6598d5f conformance

Schema conformance.nebu/v1, run 2026-11-03T09:56:58Z, 8 passed, 0 failed

| Feature | Outcome | Checks | Duration |
|---|---|---|---|
| EndpointServed | passed | 3/3 | 46ms |
| EndpointConfinement | passed | 6/6 | 54ms |
| DataParallelPortConfinement | passed | 7/7 | 25ms |
| WeightedSplit | passed | 3/3 | 129ms |
| FailOpen | passed | 6/6 | 32ms |
| InvalidPoolRef | passed | 6/6 | 15ms |
| MissingSchedulerRef | passed | 10/10 | 27ms |
| AppProtocolDefaults | passed | 6/6 | 18ms |

## EndpointServed

Every response names the endpoint that actually served it in X-Gateway-Destination-Endpoint-Served

| Check | Passed | Detail |
|---|---|---|
| served header names the serving endpoint | true | 60 of 60 answers, 20 of them streamed, named the stub that received the request, spread map[conformance-268b75-served-a:18 conformance-268b75-served-b:21 conformance-268b75-served-c:21] |
| answer comes from the named endpoint | true | every answer carried the X-Stub-Id of the stub that received it |
| every endpoint of the pool served | true | 3 of 3 stubs served at least once, map[conformance-268b75-served-a:18 conformance-268b75-served-b:21 conformance-268b75-served-c:21] |

## EndpointConfinement

X-Gateway-Destination-Endpoint-Subset confines every pick to the named endpoints and answers 503 when none is eligible

| Check | Passed | Detail |
|---|---|---|
| subset of conformance-268b75-subset-a alone | true | 20 of 20 requests served inside [127.0.0.1:43419] with a matching served header, by address map[127.0.0.1:43419:20] |
| subset of conformance-268b75-subset-b alone | true | 20 of 20 requests served inside [127.0.0.1:34795] with a matching served header, by address map[127.0.0.1:34795:20] |
| subset of conformance-268b75-subset-c alone | true | 20 of 20 requests served inside [127.0.0.1:43811] with a matching served header, by address map[127.0.0.1:43811:20] |
| subset of two endpoints | true | 20 of 20 requests served inside [127.0.0.1:43419 127.0.0.1:34795] with a matching served header, by address map[127.0.0.1:34795:8 127.0.0.1:43419:12] |
| subset naming only an endpoint outside the pool answers 503 | true | 10 of 10 answered 503, stub requests before map[conformance-268b75-subset-a:32 conformance-268b75-subset-b:29 conformance-268b75-subset-c:20 conformance-268b75-subset-outside:0] after map[conformance-268b75-subset-a:32 conformance-268b75-subset-b:29 conformance-268b75-subset-c:20 conformance-268b75-subset-outside:0] |
| subset naming only a dead address answers 503 | true | 10 of 10 answered 503, stub requests before map[conformance-268b75-subset-a:32 conformance-268b75-subset-b:29 conformance-268b75-subset-c:20 conformance-268b75-subset-outside:0] after map[conformance-268b75-subset-a:32 conformance-268b75-subset-b:29 conformance-268b75-subset-c:20 conformance-268b75-subset-outside:0] |

## DataParallelPortConfinement

Each target port of one host is its own endpoint and a port outside target_ports never serves

| Check | Passed | Detail |
|---|---|---|
| target ports read back | true | spec target_ports [38581 42329], created with [38581 42329] |
| one endpoint per target port | true | status endpoints [127.0.0.1:38581 127.0.0.1:42329], want [127.0.0.1:38581 127.0.0.1:42329] |
| concurrent traffic stays on listed ports | true | 40 of 40 answers served from [127.0.0.1:42329 127.0.0.1:38581], unlisted port 42841 received 0 |
| rank 0 port reachable alone | true | 10 of 10 requests served inside [127.0.0.1:42329] with a matching served header, by address map[127.0.0.1:42329:10] |
| rank 1 port reachable alone | true | 10 of 10 requests served inside [127.0.0.1:38581] with a matching served header, by address map[127.0.0.1:38581:10] |
| unlisted port of the same host never serves | true | 10 of 10 answered 503, stub requests before map[conformance-268b75-dp-rank0:29 conformance-268b75-dp-rank1:32 conformance-268b75-dp-unlisted:0] after map[conformance-268b75-dp-rank0:29 conformance-268b75-dp-rank1:32 conformance-268b75-dp-unlisted:0] |
| endpoint on a port outside target_ports refused | true | target_ports [42329 38581] with an endpoint at 127.0.0.1:42841 answered put pool conformance-268b75-dp-stray: invalid_argument: invalid resource: pool conformance-268b75-dp-stray static endpoint 127.0.0.1:42841 port is not a target port |

## WeightedSplit

A route splits traffic over pool targets in proportion to their weights

| Check | Passed | Detail |
|---|---|---|
| route resolves both pools | true | status pool_ids [conformance-268b75-split-heavy conformance-268b75-split-light] |
| every split request served | true | 2000 of 2000 answered 200, stubs received 2000 |
| split follows the weights | true | weight 70 pool received 1418 and weight 30 pool received 582, share 0.709 against 0.70 within 0.05 |

## FailOpen

With the scheduler unreachable a FAIL_OPEN pool still serves and a FAIL_CLOSE pool answers 503

| Check | Passed | Detail |
|---|---|---|
| remote scheduler config accepted | true | config conformance-268b75-failopen-remote at dead address 127.0.0.1:45329, Accepted TRUE/Accepted (the configuration loads) |
| pool conformance-268b75-failopen-open accepted with the remote config | true | failure mode FAILURE_MODE_FAIL_OPEN, Accepted TRUE/Accepted (the pool spec is valid) |
| pool conformance-268b75-failopen-close accepted with the remote config | true | failure mode FAILURE_MODE_FAIL_CLOSE, Accepted TRUE/Accepted (the pool spec is valid) |
| FAIL_OPEN pool serves with the scheduler down | true | first 200 after 1 tries <nil> |
| FAIL_OPEN pool keeps serving | true | 20 of 20 requests served inside [127.0.0.1:38123] with a matching served header, by address map[127.0.0.1:38123:20] |
| FAIL_CLOSE pool answers 503 with the scheduler down | true | 20 of 20 answered 503, stub requests before map[conformance-268b75-failopen-close:0 conformance-268b75-failopen-open:21] after map[conformance-268b75-failopen-close:0 conformance-268b75-failopen-open:21] |

## InvalidPoolRef

A route whose target pool does not exist reports ResolvedRefs False with reason InvalidPoolRef and answers 503

| Check | Passed | Detail |
|---|---|---|
| ResolvedRefs False with InvalidPoolRef | true | ResolvedRefs FALSE/InvalidPoolRef (target pools conformance-268b75-invalidref do not exist) |
| route stays Accepted | true | Accepted TRUE/Accepted (the route spec is valid) |
| route not Ready | true | Ready FALSE/NoReadyEndpoints (no target pool has a ready endpoint) |
| accepted route with no pool answers 503 | true | 10 of 10 answered 503, stub requests before map[conformance-268b75-invalidref:1] after map[conformance-268b75-invalidref:1] |
| ResolvedRefs True once the pool returns | true | route conformance-268b75-invalidref |
| restored route serves its pool | true | 10 of 10 requests served inside [127.0.0.1:34329] with a matching served header, by address map[127.0.0.1:34329:10] |

## MissingSchedulerRef

A pool naming an unknown scheduler config reports UnknownSchedulerConfig and answers 503 under every failure mode

| Check | Passed | Detail |
|---|---|---|
| pool conformance-268b75-missingref-fail_open Accepted False with UnknownSchedulerConfig | true | failure mode FAILURE_MODE_FAIL_OPEN, Accepted FALSE/UnknownSchedulerConfig (scheduler config conformance-268b75-missingref-config does not exist) |
| pool conformance-268b75-missingref-fail_open ResolvedRefs False with UnknownSchedulerConfig | true | parents ResolvedRefs FALSE/UnknownSchedulerConfig (scheduler config conformance-268b75-missingref-config does not exist) |
| pool conformance-268b75-missingref-fail_open fails closed under FAILURE_MODE_FAIL_OPEN | true | 10 of 10 answered 503, stub requests before map[conformance-268b75-missingref-fail_open:0 conformance-268b75-missingref-unspecified:0] after map[conformance-268b75-missingref-fail_open:0 conformance-268b75-missingref-unspecified:0] |
| pool conformance-268b75-missingref-unspecified Accepted False with UnknownSchedulerConfig | true | failure mode FAILURE_MODE_UNSPECIFIED, Accepted FALSE/UnknownSchedulerConfig (scheduler config conformance-268b75-missingref-config does not exist) |
| pool conformance-268b75-missingref-unspecified ResolvedRefs False with UnknownSchedulerConfig | true | parents ResolvedRefs FALSE/UnknownSchedulerConfig (scheduler config conformance-268b75-missingref-config does not exist) |
| pool conformance-268b75-missingref-unspecified fails closed under FAILURE_MODE_UNSPECIFIED | true | 10 of 10 answered 503, stub requests before map[conformance-268b75-missingref-fail_open:0 conformance-268b75-missingref-unspecified:0] after map[conformance-268b75-missingref-fail_open:0 conformance-268b75-missingref-unspecified:0] |
| pool conformance-268b75-missingref-fail_open Accepted once the config exists | true | Accepted TRUE/Accepted (the pool spec is valid) parents ResolvedRefs TRUE/ResolvedRefs (scheduler config conformance-268b75-missingref-config loads) |
| pool conformance-268b75-missingref-fail_open serves once the config exists | true | 10 of 10 requests served inside [127.0.0.1:33903] with a matching served header, by address map[127.0.0.1:33903:10] |
| pool conformance-268b75-missingref-unspecified Accepted once the config exists | true | Accepted TRUE/Accepted (the pool spec is valid) parents ResolvedRefs TRUE/ResolvedRefs (scheduler config conformance-268b75-missingref-config loads) |
| pool conformance-268b75-missingref-unspecified serves once the config exists | true | 10 of 10 requests served inside [127.0.0.1:44567] with a matching served header, by address map[127.0.0.1:44567:10] |

## AppProtocolDefaults

A pool created without app_protocol reads back APP_PROTOCOL_HTTP and reaches its endpoints over HTTP/1.1

| Check | Passed | Detail |
|---|---|---|
| created pool reports APP_PROTOCOL_HTTP | true | PutPool answered app_protocol APP_PROTOCOL_HTTP |
| pool reads back APP_PROTOCOL_HTTP | true | GetPool answered app_protocol APP_PROTOCOL_HTTP |
| HTTP/1.1 only stub serves the default pool | true | 10 of 10 requests served inside [127.0.0.1:42483] with a matching served header, by address map[127.0.0.1:42483:10] |
| default pool speaks HTTP/1.1 | true | stub received requests by protocol map[HTTP/1.1:11] |
| h2c stub serves the H2C pool | true | 10 of 10 requests served inside [127.0.0.1:38891] with a matching served header, by address map[127.0.0.1:38891:10] |
| H2C pool speaks HTTP/2 so the default is a choice | true | stub received requests by protocol map[HTTP/2.0:11] |
