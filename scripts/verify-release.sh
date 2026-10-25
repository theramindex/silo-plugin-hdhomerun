#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
expected_version="${1:-}"
manifest_version="$(node -p "JSON.parse(require('fs').readFileSync('manifest.json','utf8')).version")"
if [[ -n "${expected_version}" && "${manifest_version}" != "${expected_version}" ]]; then
  printf 'manifest version does not match requested release\n' >&2
  exit 1
fi
if [[ "${2:-}" == "--version-only" ]]; then exit 0; fi
go vet ./...
go test -race ./...
node --check internal/plugin/ui/app.js
go run . manifest >/dev/null
