#!/bin/sh
# Regenerates protobuf outputs and fails when they do not compile
set -eu
cd "$(dirname "$0")/.."
buf lint
buf generate
go build ./...
echo "generated code is current"
