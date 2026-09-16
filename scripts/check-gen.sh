#!/bin/sh
# Regenerate protobuf outputs and check compilation.
set -eu
cd "$(dirname "$0")/.."
buf lint
make gen
go build ./...
