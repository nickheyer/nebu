#!/bin/sh
# Fails when any package outside the standard library in the dependency graph needs cgo
set -eu
cd "$(dirname "$0")/.."
# Listing with cgo enabled keeps cgo files visible; with it off go list hides them and nothing can fail
bad=$(CGO_ENABLED=1 go list -deps -f '{{if and .CgoFiles (not .Standard)}}{{.ImportPath}}{{end}}' ./... 2>/dev/null || true)
if [ -n "$bad" ]; then
  echo "cgo dependencies found:"
  echo "$bad"
  exit 1
fi
echo "no cgo in the dependency graph"
