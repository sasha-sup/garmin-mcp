#!/usr/bin/env bash
# Builds dist/garmin-mcp.mcpb from GoReleaser output (run after `goreleaser release`).
# Usage: scripts/build-mcpb.sh <version>
# Contains macOS universal, Windows amd64 and Linux amd64 binaries: MCPB selects
# by OS only, not by CPU architecture.
set -euo pipefail
version=${1:?usage: scripts/build-mcpb.sh <version, e.g. 0.1.0>}
mcpb="npx -y @anthropic-ai/mcpb@2.1.2"

bin() { # bin <goos> <goarch>: path of a GoReleaser binary from dist/artifacts.json
  local p
  p=$(jq -r --arg os "$1" --arg arch "$2" \
    '.[] | select((.type == "Binary" or .type == "Universal Binary") and .goos == $os and .goarch == $arch) | .path' dist/artifacts.json)
  [[ -n $p ]] || { echo "error: no $1/$2 binary in dist/artifacts.json; run goreleaser first" >&2; exit 1; }
  echo "$p"
}

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/server"
install -m 0755 "$(bin darwin all)" "$stage/server/garmin-mcp-darwin"
install -m 0755 "$(bin linux amd64)" "$stage/server/garmin-mcp-linux"
install -m 0755 "$(bin windows amd64)" "$stage/server/garmin-mcp-windows.exe"
cp LICENSE "$stage/"
jq --arg v "$version" '.version = $v' packaging/mcpb/manifest.json > "$stage/manifest.json"

$mcpb validate "$stage/manifest.json"
$mcpb pack "$stage" dist/garmin-mcp.mcpb
$mcpb info dist/garmin-mcp.mcpb
