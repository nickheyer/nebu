#!/bin/sh
# Reject cgo dependencies outside the standard library.
set -eu
cd "$(dirname "$0")/.."
# Enable cgo so go list includes cgo files.
bad=$(CGO_ENABLED=1 go list -deps -f '{{if and .CgoFiles (not .Standard)}}{{.ImportPath}}{{end}}' ./...)
if [ -n "$bad" ]; then
  echo "cgo dependencies found:"
  echo "$bad"
  exit 1
fi
echo "no cgo in the dependency graph"
