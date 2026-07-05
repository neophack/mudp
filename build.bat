@echo off
setlocal enabledelayedexpansion

set OUTDIR=dist
if not "%~1"=="" set OUTDIR=%~1

if not exist "%OUTDIR%" mkdir "%OUTDIR%"

go build -trimpath -ldflags "-s -w" -o "%OUTDIR%\mudp.exe" .\cmd\mudp
if errorlevel 1 (
  echo build failed
  exit /b 1
)

echo Built %OUTDIR%\mudp.exe
