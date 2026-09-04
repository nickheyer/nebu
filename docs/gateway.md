# Gateway

The gateway is a reverse proxy under `/v1/` and `/api/` on the API listener, or on its own
address when `gateway.listen` is set, that answers in the OpenAI, Anthropic, and Ollama wire
formats whatever the runtime behind a route speaks. Point a router or client at it once.

Requests are routed by the `model` field of the JSON body, the `X-Nebu-Model` header, or
the `model` query parameter. With exactly one route ready and no model named, that route is
used. `/v1/models` lists every route with whether it is ready and `/v1/models/NAME` answers
one, both in Anthropic's shape when the request carries an `anthropic-version` header, and
`/api/tags`, `/api/ps`, and `/api/show` describe them the way Ollama does, every route taking
tools. `/health` is open, and a gateway on its own listener answers `/` with the heartbeat the
Ollama CLI checks before it talks. Bodies are capped at 64 MiB and refused past it.

## Flavors

A runtime manifest names the format its server speaks in `launch.api`, OpenAI for every one
shipped. The path a request arrives on says which format the client speaks: `/v1/messages` is
Anthropic, anything under `/api/` is Ollama, and the rest of `/v1/` is OpenAI. When the two
match the request and response pass through byte for byte, streaming included. When they differ
the gateway reads the request into one canonical chat, writes it in the runtime's format, and
turns the answer back, streamed or not: text, images, tool definitions, tool calls and their
results, stop reasons, and token counts all cross over. Anthropic's `/v1/messages` streams as its
own event sequence, Ollama's `/api/chat` and `/api/generate` as newline delimited JSON, and
`/api/embed` maps onto embeddings. Errors come back in the caller's shape too. A runtime that
speaks Anthropic or Ollama natively would be served the same way in the other direction.

`/v1/messages/count_tokens` counts a prompt. An OpenAI runtime is asked through the
`/tokenize` route llama.cpp, vLLM, and SGLang serve beside their chat endpoints, and an Anthropic
runtime through its own count endpoint. When the runtime has neither, or refuses, the gateway
answers an estimate: four bytes a token, a few per turn, and each image by its area, so a
client budgeting context never gets an error for asking.

| status | meaning |
| --- | --- |
| 200 | proxied to the instance, streaming passes through |
| 401 | `gateway.api_keys` is set and no valid bearer key was sent |
| 404 | no route has that name |
| 413 | the request body is over 64 MiB |
| 429 | the route has every allowed request in flight, or is over its request rate, retry after the header says |
| 503 | the route exists but nothing serves it yet, or it is being replaced, retry after the header says |
| 504 | the runtime did not start answering within the upstream timeout, or the whole exchange outran the request timeout |

Routes come from three places. A plain `nebu run` registers its instance name when it is
ready and drops it when the instance ends. A slot keeps its name for its whole life and
points it at the current occupant. `nebu routes add NAME INSTANCE` adds an alias onto a
running instance.

`nebu gateway` shows listeners, routes with request and in flight counts, the limits each
enforces, and whether keys are required. Counters feed the swap logic: an instance is drained
by waiting for its in flight count to reach zero.

## Browsers and TLS

Every gateway path answers CORS preflights, allowing whatever headers the caller's SDK asks
to send, so a page on another origin, a chat UI or a notebook, can call it with its key.
`gateway.cors_origins` narrows which origins are allowed; empty allows any, which is safe
because the key is still required. The listeners are plain
HTTP unless `tls.cert_file` and `tls.key_file` are set, in which case the API, the web UI, and
the gateway all speak TLS with HTTP/2. Listening beyond loopback without TLS, or without a
token or keys, is logged as a warning at start. A reverse proxy in front of the loopback
listener works as well; it only has to pass the `Authorization` header through and leave
streamed responses unbuffered.

## Limits

Every route enforces a policy: requests in flight at once, a sustained request rate with a
burst, a timeout on the whole exchange, and an upstream timeout, how long the runtime may take
to start answering. `gateway.policy` in config sets the default for every route, and a slot
carries its own for its route, each zero field inheriting the gateway's. A plain `nebu run`
route follows the default. The upstream timeout defaults to ten minutes so a hung runtime
returns `504` and releases its in flight count instead of holding a client and a drain
forever; every other limit is off until set. Rate limiting is a token bucket per route, the
burst defaulting to the rate rounded up.
