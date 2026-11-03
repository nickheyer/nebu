#!/usr/bin/env bash
# Runs the conformance suite against a fresh nebu daemon on loopback and writes its report
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
bin=$(realpath "${1:-$root/build/nebu}")
reports=${REPORTS:-$root/conformance/reports}
work=$(mktemp -d "${TMPDIR:-/tmp}/nebu-conformance.XXXXXX")
data=$work/data
mkdir -p "$data" "$work/cache"
echo '{}' >"$work/config.yaml"

NEBU_DATA_DIR=$data NEBU_CACHE_DIR=$work/cache NEBU_LISTEN=127.0.0.1:0 NEBU_MESH_ANNOUNCE=false \
	"$bin" --config "$work/config.yaml" serve >"$work/daemon.log" 2>&1 &
pid=$!

# Stops the daemon, printing its log when the run failed, and removes the scratch directory
finish() {
	status=$?
	set +e
	kill -TERM "$pid" 2>/dev/null && wait "$pid" 2>/dev/null
	if [ "$status" -ne 0 ]; then
		echo "conformance: daemon log follows" >&2
		tail -n 60 "$work/daemon.log" >&2
	fi
	rm -rf "$work"
	exit "$status"
}
trap finish EXIT

addr=""
for _ in $(seq 1 300); do
	kill -0 "$pid" 2>/dev/null || { echo "conformance: daemon exited before listening" >&2; exit 1; }
	if [ -z "$addr" ] && [ -s "$data/daemon.lock" ]; then
		addr=$(head -n 1 "$data/daemon.lock")
	fi
	if [ -n "$addr" ] && [ -s "$data/tls.crt" ] && curl -fsS --cacert "$data/tls.crt" -o /dev/null "https://$addr/readyz" 2>/dev/null; then
		break
	fi
	sleep 0.1
done
curl -fsS --cacert "$data/tls.crt" -o /dev/null "https://$addr/readyz"

cd "$root"
go test -v -count=1 -tags conformance ./conformance -args \
	-gateway-url="https://$addr" \
	-api-url="https://$addr" \
	-token="$(cat "$data/api.token")" \
	-tls-ca="$data/tls.crt" \
	-report="$reports"
