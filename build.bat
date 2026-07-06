@echo off
setlocal enabledelayedexpansion

set OUTDIR=dist
if not "%~1"=="" set OUTDIR=%~1

if not exist "%OUTDIR%" mkdir "%OUTDIR%"

set GOOS=windows
set GOARCH=amd64
go build -trimpath -ldflags "-s -w" -o "%OUTDIR%\mudp.exe" .\cmd\mudp
if errorlevel 1 (
  echo build windows failed
  exit /b 1
)
echo Built %OUTDIR%\mudp.exe

set GOOS=linux
set GOARCH=amd64
go build -trimpath -ldflags "-s -w" -o "%OUTDIR%\mudp" .\cmd\mudp
if errorlevel 1 (
  echo build linux failed
  exit /b 1
)
echo Built %OUTDIR%\mudp
