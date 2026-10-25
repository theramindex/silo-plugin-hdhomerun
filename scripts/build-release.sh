#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
version="$(node -p "JSON.parse(require('fs').readFileSync('manifest.json','utf8')).version")"
mkdir -p dist
for target in linux/amd64 linux/arm64 darwin/arm64; do
  target_os="${target%/*}"
  target_arch="${target#*/}"
  binary="dist/hdhomerun-${version}-${target_os}-${target_arch}"
  CGO_ENABLED=0 GOOS="${target_os}" GOARCH="${target_arch}" go build -trimpath -ldflags "-s -w -X main.buildVersion=${version}" -o "${binary}" .
  go run ./cmd/package-upload -binary "${binary}" -version "${version}" -goos "${target_os}" -goarch "${target_arch}"
done
