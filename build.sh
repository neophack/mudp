#!/usr/bin/env bash
set -euo pipefail

OUTDIR="${1:-dist}"
mkdir -p "$OUTDIR"

# The web console is embedded via go:embed, so it must be built first.
echo "Building web frontend..."
npm --prefix web run build

GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"

# Release assets follow the openp2p convention (internal/upgrader.AssetName):
# <name>-<os>-<arch>, .exe on Windows; the release workflow packages them as
# versioned zip / tar.gz archives.
EXT=""
[ "$GOOS" = "windows" ] && EXT=".exe"
BASE="mudp-${GOOS}-${GOARCH}"

OUTPUT="$OUTDIR/${BASE}${EXT}"
# The version constant in internal/version/version.go is the single source
# of truth (openp2p-style: no build-time injection).
VERSION=$(grep -oP 'var Version = "\K[^"]+' internal/version/version.go)

echo "Building mudp for $GOOS/$GOARCH (version $VERSION)..."
GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath -ldflags "-s -w" -o "$OUTPUT" ./cmd/mudp

echo "Built $OUTPUT"
