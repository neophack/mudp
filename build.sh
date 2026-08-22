#!/usr/bin/env bash
set -euo pipefail

OUTDIR="${1:-dist}"
mkdir -p "$OUTDIR"

GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"

# Release asset names are fixed per platform (internal/upgrader.AssetName):
# mudp_x86.exe / mudp_x86_linux (amd64), mudp_arm64.exe / mudp_arm64_linux
# (arm64); other targets fall back to a suffixed name.
EXT=""
case "$GOOS/$GOARCH" in
  windows/amd64) BASE="mudp_x86"; EXT=".exe" ;;
  linux/amd64)   BASE="mudp_x86_linux" ;;
  windows/arm64) BASE="mudp_arm64"; EXT=".exe" ;;
  linux/arm64)   BASE="mudp_arm64_linux" ;;
  *)             BASE="mudp-${GOOS}-${GOARCH}"; [ "$GOOS" = "windows" ] && EXT=".exe" ;;
esac

OUTPUT="$OUTDIR/${BASE}${EXT}"
VERSION=$(git describe --tags --always 2>/dev/null || echo dev)

echo "Building mudp for $GOOS/$GOARCH (version $VERSION)..."
GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath -ldflags "-s -w -X mudp/internal/version.Version=$VERSION" -o "$OUTPUT" ./cmd/mudp

echo "Built $OUTPUT"
