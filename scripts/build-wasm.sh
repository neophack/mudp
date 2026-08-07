#!/usr/bin/env bash
# Builds web/wasm/raster.wasm (the Go-compiled YUV/RAW pixel decoder used by
# web/lib/wasmRaster.js) and copies the matching wasm_exec.js glue next to it.
# web/embed.go embeds both via go:embed, so this must run before any
# `go build`/`go vet`/`go test` on the mudp module.
set -euo pipefail

OUTDIR="web/wasm"
mkdir -p "$OUTDIR"

echo "Building $OUTDIR/raster.wasm..."
GOOS=js GOARCH=wasm go build -trimpath -o "$OUTDIR/raster.wasm" ./cmd/rasterwasm

GOROOT=$(go env GOROOT)
WASM_EXEC=""
for candidate in "$GOROOT/lib/wasm/wasm_exec.js" "$GOROOT/misc/wasm/wasm_exec.js"; do
  if [ -f "$candidate" ]; then
    WASM_EXEC="$candidate"
    break
  fi
done
if [ -z "$WASM_EXEC" ]; then
  echo "could not find wasm_exec.js under $GOROOT (checked lib/wasm and misc/wasm)" >&2
  exit 1
fi
cp "$WASM_EXEC" "$OUTDIR/wasm_exec.js"

echo "Built $OUTDIR/raster.wasm and $OUTDIR/wasm_exec.js"
