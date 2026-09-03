#!/bin/sh
# Builds static binaries for every supported platform into build/release
set -eu
cd "$(dirname "$0")/.."
version=${1:-$(git describe --tags --always --dirty 2>/dev/null || echo devel)}
out=build/release
rm -rf "$out"
mkdir -p "$out"
export CGO_ENABLED=0
for target in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64; do
  os=${target%/*}
  arch=${target#*/}
  name="nebu-$version-$os-$arch"
  echo "building $name"
  GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "$out/$name/nebu" ./cmd/nebu
  tar -C "$out" -czf "$out/$name.tar.gz" "$name"
  rm -rf "$out/$name"
done
(cd "$out" && sha256sum *.tar.gz > SHA256SUMS)
echo "release archives in $out"
