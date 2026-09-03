# Gateway

The gateway is an OpenAI compatible reverse proxy under `/v1/` on the API listener, or on
its own address when `gateway.listen` is set. Point a router or client at it once.

Requests are routed by the `model` field of the JSON body, the `X-Nebu-Model` header, or
the `model` query parameter. With exactly one route ready and no model named, that route is
used. `/v1/models` lists every route with whether it is ready. `/health` is open.

| status | meaning |
| --- | --- |
| 200 | proxied to the instance, streaming passes through |
| 401 | `gateway.api_keys` is set and no valid bearer key was sent |
| 404 | no route has that name |
| 503 | the route exists but nothing serves it yet, or it is being replaced, retry after the header says |

Routes come from three places. A plain `nebu run` registers its instance name when it is
ready and drops it when the instance ends. A slot keeps its name for its whole life and
points it at the current occupant. `nebu routes add NAME INSTANCE` adds an alias onto a
running instance.

`nebu gateway` shows listeners, routes with request and in flight counts, and whether keys
are required. Counters feed the swap logic: an instance is drained by waiting for its in
flight count to reach zero.
