param(
  [string]$OutDir = "dist"
)

$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

go build -trimpath -ldflags "-s -w" -o "$OutDir/mudp.exe" ./cmd/mudp

Write-Host "Built $OutDir/mudp.exe"
