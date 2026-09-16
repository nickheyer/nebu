#!/bin/sh
# Build release artifacts locally without publishing anything.
set -eu
cd "$(dirname "$0")/.."
exec "${GORELEASER:-goreleaser}" release --snapshot --clean --skip=publish,docker "$@"
