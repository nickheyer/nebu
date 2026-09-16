# syntax=docker/dockerfile:1
ARG BASE_IMAGE=ubuntu:24.04

FROM --platform=$BUILDPLATFORM bufbuild/buf:1.73.0 AS proto
WORKDIR /src
COPY buf.yaml buf.gen.yaml ./
COPY proto ./proto
RUN buf generate

FROM --platform=$BUILDPLATFORM node:22-bookworm-slim AS web
WORKDIR /src/web/nebu
COPY web/nebu/package.json web/nebu/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/nebu ./
COPY --from=proto /src/web/nebu/src/lib/proto ./src/lib/proto
COPY --from=proto /src/web/nebu/static/openapi.yaml ./static/openapi.yaml
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=proto /src/pkg/proto ./pkg/proto
COPY --from=web /src/web/nebu/dist ./web/nebu/dist
ARG TARGETOS
ARG TARGETARCH
ARG NEBU_VERSION=devel
ARG NEBU_COMMIT
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    test -s web/nebu/dist/index.html && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -mod=readonly \
      -ldflags "-s -w -X github.com/nickheyer/nebu/internal/cli.releaseVersion=$NEBU_VERSION -X github.com/nickheyer/nebu/internal/cli.releaseCommit=$NEBU_COMMIT" \
      -o /out/nebu ./cmd/nebu

FROM docker:28-cli AS docker-cli

FROM ${BASE_IMAGE} AS runtime
USER root
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ca-certificates curl git git-lfs build-essential cmake ninja-build pkg-config \
      python3 python3-dev python3-pip python3-venv libgomp1 libnuma1 libnuma-dev \
      libssl-dev libcurl4-openssl-dev libvulkan1 procps tini tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 nebu \
    && useradd --uid 10001 --gid nebu --home-dir /var/lib/nebu --no-create-home nebu \
    && install -d -o nebu -g nebu /var/lib/nebu /var/cache/nebu /etc/nebu
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY packaging/config.yaml /etc/nebu/config.yaml
ENV HOME=/var/lib/nebu \
    NEBU_DATA_DIR=/var/lib/nebu \
    NEBU_LISTEN=0.0.0.0:8484 \
    XDG_CACHE_HOME=/var/cache/nebu \
    HF_HOME=/var/cache/nebu/huggingface \
    TORCH_HOME=/var/cache/nebu/torch \
    PIP_CACHE_DIR=/var/cache/nebu/pip \
    PYTHONUNBUFFERED=1
WORKDIR /var/lib/nebu
USER 10001:10001
EXPOSE 8484
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD curl --fail --silent --show-error http://127.0.0.1:8484/health || exit 1
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/nebu"]
CMD ["serve"]

FROM runtime AS release
ARG TARGETPLATFORM
COPY --chmod=755 ${TARGETPLATFORM}/nebu /usr/local/bin/nebu

FROM runtime AS final
COPY --from=build --chmod=755 /out/nebu /usr/local/bin/nebu
