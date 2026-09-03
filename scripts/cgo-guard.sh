#!/bin/sh
# Fails when any package in the dependency graph needs cgo
set -eu
cd "$(dirname "$0")/.."
export CGO_ENABLED=0
bad=$(go list -deps ./... | xargs go list -f '{{if .CgoFiles}}{{.ImportPath}}{{end}}' 2>/dev/null || true)
if [ -n "$bad" ]; then
  echo "cgo dependencies found:"
  echo "$bad"
  exit 1
fi
echo "no cgo in the dependency graph"
