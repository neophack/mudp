@echo off
setlocal enabledelayedexpansion

set OUTDIR=dist
if not "%~1"=="" set OUTDIR=%~1

if not exist "%OUTDIR%" mkdir "%OUTDIR%"

powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build-wasm.ps1
if errorlevel 1 (
  echo build-wasm failed
  exit /b 1
)

for /f %%i in ('git rev-parse --short HEAD 2^>nul') do set COMMIT=%%i
if not defined COMMIT set COMMIT=dev

set GOOS=windows
set GOARCH=amd64
go build -trimpath -ldflags "-s -w -X mudp/internal/version.Version=%COMMIT%" -o "%OUTDIR%\mudp.exe" .\cmd\mudp
if errorlevel 1 (
  echo build windows failed
  exit /b 1
)
echo Built %OUTDIR%\mudp.exe

set GOOS=linux
set GOARCH=amd64
go build -trimpath -ldflags "-s -w -X mudp/internal/version.Version=%COMMIT%" -o "%OUTDIR%\mudp" .\cmd\mudp
if errorlevel 1 (
  echo build linux failed
  exit /b 1
)
echo Built %OUTDIR%\mudp
