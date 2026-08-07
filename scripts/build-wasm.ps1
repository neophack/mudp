# Builds web/wasm/raster.wasm (the Go-compiled YUV/RAW pixel decoder used by
# web/lib/wasmRaster.js) and copies the matching wasm_exec.js glue next to it.
# web/embed.go embeds both via go:embed, so this must run before any
# `go build`/`go vet`/`go test` on the mudp module.
$ErrorActionPreference = "Stop"

$OutDir = "web/wasm"
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

Write-Host "Building $OutDir/raster.wasm..."
$env:GOOS = "js"
$env:GOARCH = "wasm"
go build -trimpath -o "$OutDir/raster.wasm" ./cmd/rasterwasm
if ($LASTEXITCODE -ne 0) { throw "build rasterwasm failed" }
Remove-Item Env:\GOOS
Remove-Item Env:\GOARCH

$GoRoot = go env GOROOT
$WasmExec = $null
foreach ($candidate in @("$GoRoot/lib/wasm/wasm_exec.js", "$GoRoot/misc/wasm/wasm_exec.js")) {
  if (Test-Path $candidate) { $WasmExec = $candidate; break }
}
if (-not $WasmExec) { throw "could not find wasm_exec.js under $GoRoot (checked lib/wasm and misc/wasm)" }
Copy-Item -Force $WasmExec "$OutDir/wasm_exec.js"

Write-Host "Built $OutDir/raster.wasm and $OutDir/wasm_exec.js"
