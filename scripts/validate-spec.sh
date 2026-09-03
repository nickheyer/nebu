#!/bin/sh
# Loads every embedded spec file the way the daemon does
set -eu
cd "$(dirname "$0")/.."
go test ./pkg/spec/ -run 'TestEmbeddedSpecsCompile|TestNoOneOffsInGo' -count=1
echo "spec files validate"
