#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
tmp=$(mktemp -d)
export NEBU_DATA_DIR="$tmp/data" NEBU_CACHE_DIR="$tmp/cache" NEBU_MODELS_DIR="$tmp/models"
export NEBU_UID NEBU_GID
NEBU_UID=$(id -u)
NEBU_GID=$(id -g)
export NEBU_IMAGE_TAG="${1:-smoke}" NEBU_PORT=0 NEBU_BIND_ADDRESS=127.0.0.1
export NEBU_AUTH_TOKEN=nebu-smoke-token NEBU_GATEWAY_API_KEYS=nebu-smoke-key
mkdir -p "$NEBU_DATA_DIR" "$NEBU_CACHE_DIR" "$NEBU_MODELS_DIR"
compose() { docker compose --env-file /dev/null --project-name "nebu-smoke-$$" -f compose.yaml "$@"; }
cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then compose logs || true; fi
  compose down --volumes || true
  rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 1' INT TERM
compose up --detach --wait --wait-timeout 120
address=$(compose port nebu 8484)
python3 scripts/smoke.py --url "http://$address"
test -s "$NEBU_DATA_DIR/nebu.db"
printf 'persistent\n' > "$NEBU_DATA_DIR/smoke-marker"
compose restart nebu
address=$(compose port nebu 8484)
python3 scripts/smoke.py --url "http://$address"
test "$(cat "$NEBU_DATA_DIR/smoke-marker")" = persistent
echo "Compose bind mounts and daemon restart: OK"
