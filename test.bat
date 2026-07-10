@echo off
setlocal enabledelayedexpansion

:: mudp all-in-one test runner
:: Usage: test.bat [go|web]
::   go   - run Go backend tests only
::   web  - run web frontend tests only
::   (none) - run both Go and web tests

set "SCOPE=%~1"
set "FAILED=0"

where go >nul 2>nul
if errorlevel 1 (
    echo [test] go is not installed or not in PATH
    exit /b 1
)

if /I "%SCOPE%"=="web" goto :web_tests

echo [test] running Go tests...
go test ./...
if errorlevel 1 (
    echo [test] Go tests failed
    set "FAILED=1"
) else (
    echo [test] Go tests passed
)

if /I "%SCOPE%"=="go" goto :summary

:web_tests
where npm >nul 2>nul
if errorlevel 1 (
    echo [test] npm is not installed or not in PATH
    exit /b 1
)

pushd web
echo [test] running web tests...
npm test
if errorlevel 1 (
    echo [test] web tests failed
    set "FAILED=1"
) else (
    echo [test] web tests passed
)
popd

:summary
if "%FAILED%"=="1" (
    echo [test] some tests failed
    exit /b 1
)

echo [test] all tests passed
exit /b 0
