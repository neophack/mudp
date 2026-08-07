param(
  [string]$OutDir = "dist"
)

$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

& "$PSScriptRoot/build-wasm.ps1"

$Commit = git rev-parse --short HEAD 2>$null
if (-not $Commit) { $Commit = "dev" }
$LdFlags = "-s -w -X mudp/internal/version.Version=$Commit"

# Same dual output as build.bat: Windows and Linux amd64 binaries.
$env:GOARCH = "amd64"

$env:GOOS = "windows"
go build -trimpath -ldflags $LdFlags -o "$OutDir/mudp.exe" ./cmd/mudp
if ($LASTEXITCODE -ne 0) { throw "build windows failed" }
Write-Host "Built $OutDir/mudp.exe"

$env:GOOS = "linux"
go build -trimpath -ldflags $LdFlags -o "$OutDir/mudp" ./cmd/mudp
if ($LASTEXITCODE -ne 0) { throw "build linux failed" }
Write-Host "Built $OutDir/mudp"
