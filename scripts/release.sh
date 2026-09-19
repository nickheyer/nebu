#!/bin/sh
# Build local release artifacts.
set -eu
cd "$(dirname "$0")/.."
exec "${GORELEASER:-goreleaser}" release --snapshot --clean --skip=publish,docker "$@"
