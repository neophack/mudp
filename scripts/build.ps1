param(
  [string]$OutDir = "dist"
)

$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

$Version = git describe --tags --always 2>$null
if (-not $Version) { $Version = "dev" }
$LdFlags = "-s -w -X mudp/internal/version.Version=$Version"

# Same four release assets as build.bat: windows/linux x amd64/arm64.
foreach ($target in @(
  @{ Goos = "windows"; Goarch = "amd64"; Out = "mudp_x86.exe" },
  @{ Goos = "linux";   Goarch = "amd64"; Out = "mudp_x86_linux" },
  @{ Goos = "windows"; Goarch = "arm64"; Out = "mudp_arm64.exe" },
  @{ Goos = "linux";   Goarch = "arm64"; Out = "mudp_arm64_linux" }
)) {
  $env:GOOS = $target.Goos
  $env:GOARCH = $target.Goarch
  go build -trimpath -ldflags $LdFlags -o "$OutDir/$($target.Out)" ./cmd/mudp
  if ($LASTEXITCODE -ne 0) { throw "build $($target.Goos)/$($target.Goarch) failed" }
  Write-Host "Built $OutDir/$($target.Out)"
}
