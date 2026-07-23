@echo off
setlocal enabledelayedexpansion

:: mudp WRT integration test runner
::
:: Tests available:
::   ping      - deploy WRT + alpine, verify AUTO-injected default route via WRT
::                (.2) + ping 8.8.8.8 + www.baidu.com (default)
::   netshoot  - deploy WRT + create a container via the PRODUCTION CreateContainer
::                path (AttachDefaultLAN=true, NET_RAW dropped) and verify
::                auto-routing + TCP egress (curl) through WRT — no manual
::                `ip route replace`, no manual resolv.conf
::   all       - run both tests
::
:: Usage:
::   test-integration.bat                 -- run 'ping' test
::   test-integration.bat netshoot        -- run production-path auto-route test
::   test-integration.bat all             -- run both tests
::   test-integration.bat -timeout 20m    -- override timeout for default test
::   test-integration.bat netshoot -timeout 20m

set "TIMEOUT=15m"
set "PKG=./internal/dockerx/"
set "SCENE=%~1"

:: Parse arguments
if /I "%SCENE%"=="-timeout" (
    set "SCENE=ping"
    if not "%~2"=="" set "TIMEOUT=%~2"
) else if /I "%SCENE%"=="netshoot" (
    if /I "%~2"=="-timeout" if not "%~3"=="" set "TIMEOUT=%~3"
) else if /I "%SCENE%"=="all" (
    if /I "%~2"=="-timeout" if not "%~3"=="" set "TIMEOUT=%~3"
) else if /I "%SCENE%"=="ping" (
    if /I "%~2"=="-timeout" if not "%~3"=="" set "TIMEOUT=%~3"
) else (
    set "SCENE=ping"
    if not "%~2"=="" if /I "%~1"=="-timeout" set "TIMEOUT=%~2"
)

:: Map scene to test filter
if /I "%SCENE%"=="netshoot" (
    set "TEST_FILTER=TestWRTAutoRouteViaGateway"
    set "SCENE_LABEL=Production path (CreateContainer + auto-route via WRT, TCP egress)"
) else if /I "%SCENE%"=="all" (
    set "TEST_FILTER=TestWRT"
    set "SCENE_LABEL=All WRT integration tests"
) else (
    set "TEST_FILTER=TestWRTDeployAndPing"
    set "SCENE_LABEL=Alpine auto-route + ping (8.8.8.8 + www.baidu.com)"
)

echo.
echo =====================================================
echo  mudp WRT Integration Test
echo  Scene   : %SCENE_LABEL%
echo  Filter  : %TEST_FILTER%
echo  Timeout : %TIMEOUT%
echo =====================================================
echo.

:: Sanity-check: Go must be available
where go >nul 2>nul
if errorlevel 1 (
    echo [ERROR] go is not installed or not in PATH
    exit /b 1
)

:: Sanity-check: Docker must be reachable
docker info >nul 2>nul
if errorlevel 1 (
    echo [ERROR] Docker daemon is not running or not reachable.
    echo         Start Docker Desktop ^(or the Docker daemon^) and retry.
    exit /b 1
)

echo [info] Docker OK — starting integration test...
echo.

go test -v -tags integration -timeout %TIMEOUT% %PKG% -run %TEST_FILTER%
set "EXIT=%ERRORLEVEL%"

echo.
if %EXIT%==0 (
    echo =====================================================
    echo  PASS  %TEST_FILTER%
    echo =====================================================
) else (
    echo =====================================================
    echo  FAIL  %TEST_FILTER%  ^(exit %EXIT%^)
    echo =====================================================
)

exit /b %EXIT%

