@echo off
setlocal enabledelayedexpansion

set OUTDIR=dist
if not "%~1"=="" set OUTDIR=%~1

if not exist "%OUTDIR%" mkdir "%OUTDIR%"

rem The web console is embedded via go:embed, so it must be built first.
echo Building web frontend...
call npm --prefix web run build
if errorlevel 1 ( echo web build failed & exit /b 1 )

for /f "delims=" %%i in ('go run .\cmd\mudp -version') do set VERSION=%%i
if not defined VERSION set VERSION=v0.0.0

rem No ldflags injection: the version constant in internal\version\version.go
rem is the single source of truth (openp2p-style).
set LDFLAGS=-s -w

rem Release assets follow the openp2p convention: <name>-<os>-<arch>[-vX.Y.Z]
rem binaries packaged as zip (windows) / tar.gz (linux); see internal/upgrader.

set GOARCH=amd64
set GOOS=windows
go build -trimpath -ldflags "%LDFLAGS%" -o "%OUTDIR%\mudp-windows-amd64.exe" .\cmd\mudp
if errorlevel 1 ( echo build windows amd64 failed & exit /b 1 )
powershell -NoProfile -Command "Compress-Archive -Path '%OUTDIR%\mudp-windows-amd64.exe' -DestinationPath '%OUTDIR%\mudp-windows-amd64-%VERSION%.zip' -Force"
echo Built %OUTDIR%\mudp-windows-amd64.exe + mudp-windows-amd64-%VERSION%.zip

set GOOS=linux
go build -trimpath -ldflags "%LDFLAGS%" -o "%OUTDIR%\mudp-linux-amd64" .\cmd\mudp
if errorlevel 1 ( echo build linux amd64 failed & exit /b 1 )
tar -czf "%OUTDIR%\mudp-linux-amd64-%VERSION%.tar.gz" -C "%OUTDIR%" mudp-linux-amd64
echo Built %OUTDIR%\mudp-linux-amd64 + mudp-linux-amd64-%VERSION%.tar.gz

set GOARCH=arm64
set GOOS=windows
go build -trimpath -ldflags "%LDFLAGS%" -o "%OUTDIR%\mudp-windows-arm64.exe" .\cmd\mudp
if errorlevel 1 ( echo build windows arm64 failed & exit /b 1 )
powershell -NoProfile -Command "Compress-Archive -Path '%OUTDIR%\mudp-windows-arm64.exe' -DestinationPath '%OUTDIR%\mudp-windows-arm64-%VERSION%.zip' -Force"
echo Built %OUTDIR%\mudp-windows-arm64.exe + mudp-windows-arm64-%VERSION%.zip

set GOOS=linux
go build -trimpath -ldflags "%LDFLAGS%" -o "%OUTDIR%\mudp-linux-arm64" .\cmd\mudp
if errorlevel 1 ( echo build linux arm64 failed & exit /b 1 )
tar -czf "%OUTDIR%\mudp-linux-arm64-%VERSION%.tar.gz" -C "%OUTDIR%" mudp-linux-arm64
echo Built %OUTDIR%\mudp-linux-arm64 + mudp-linux-arm64-%VERSION%.tar.gz
