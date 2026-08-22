@echo off
setlocal enabledelayedexpansion

set OUTDIR=dist
if not "%~1"=="" set OUTDIR=%~1

if not exist "%OUTDIR%" mkdir "%OUTDIR%"

for /f "delims=" %%i in ('git describe --tags --always 2^>nul') do set VERSION=%%i
if not defined VERSION set VERSION=dev

set LDFLAGS=-s -w -X mudp/internal/version.Version=%VERSION%

set GOARCH=amd64
set GOOS=windows
go build -trimpath -ldflags "%LDFLAGS%" -o "%OUTDIR%\mudp_x86.exe" .\cmd\mudp
if errorlevel 1 ( echo build windows amd64 failed & exit /b 1 )
echo Built %OUTDIR%\mudp_x86.exe

set GOOS=linux
go build -trimpath -ldflags "%LDFLAGS%" -o "%OUTDIR%\mudp_x86_linux" .\cmd\mudp
if errorlevel 1 ( echo build linux amd64 failed & exit /b 1 )
echo Built %OUTDIR%\mudp_x86_linux

set GOARCH=arm64
set GOOS=windows
go build -trimpath -ldflags "%LDFLAGS%" -o "%OUTDIR%\mudp_arm64.exe" .\cmd\mudp
if errorlevel 1 ( echo build windows arm64 failed & exit /b 1 )
echo Built %OUTDIR%\mudp_arm64.exe

set GOOS=linux
go build -trimpath -ldflags "%LDFLAGS%" -o "%OUTDIR%\mudp_arm64_linux" .\cmd\mudp
if errorlevel 1 ( echo build linux arm64 failed & exit /b 1 )
echo Built %OUTDIR%\mudp_arm64_linux
