#!/usr/bin/env bash
set -euo pipefail

OUTDIR="${1:-dist}"
mkdir -p "$OUTDIR"

GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"

EXT=""
if [ "$GOOS" = "windows" ]; then
  EXT=".exe"
fi

HOST_OS=$(go env GOHOSTOS)
HOST_ARCH=$(go env GOHOSTARCH)
SUFFIX=""
if [ "$GOOS" != "$HOST_OS" ] || [ "$GOARCH" != "$HOST_ARCH" ]; then
  SUFFIX="-${GOOS}-${GOARCH}"
fi

OUTPUT="$OUTDIR/mudp${SUFFIX}${EXT}"

echo "Building mudp for $GOOS/$GOARCH..."
GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath -ldflags "-s -w" -o "$OUTPUT" ./cmd/mudp

echo "Built $OUTPUT"
