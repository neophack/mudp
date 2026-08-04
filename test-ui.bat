@echo off
setlocal enabledelayedexpansion

:: mudp web UI click-through test runner
:: Builds the binary, then drives a real browser through every sidebar tab and
:: every page-level button as both an admin and a plain "user" account, plus
:: the first-run setup wizard.
:: Usage: test-ui.bat [admin|user|responsive|workflows|sharedisk|disktabs]
::   admin      - run the admin console click-through plus the setup wizard
::   user       - run only the user console click-through
::   responsive - run only the phone/tablet layout smoke test
::   workflows  - run only the multi-step user-operation workflow tests
::   sharedisk  - run only the shared-disk / bind-mount container tests
::   disktabs   - run only the backup/shared disk visibility tests
::   (none)     - run everything

set "SCOPE=%~1"

:: Anchor to the repo root regardless of the caller's cwd, since the build and
:: pushd web below are both relative to it.
cd /d "%~dp0"

where go >nul 2>nul
if errorlevel 1 (
    echo [test-ui] go is not installed or not in PATH
    exit /b 1
)

where npx >nul 2>nul
if errorlevel 1 (
    echo [test-ui] npm/npx is not installed or not in PATH
    exit /b 1
)

echo [test-ui] building dist\mudp.exe...
go build -o dist\mudp.exe .\cmd\mudp
if errorlevel 1 (
    echo [test-ui] build failed
    exit /b 1
)

:: Forward slashes only: Playwright matches spec arguments as regexes against
:: POSIX-style paths, so a backslash here silently matches zero tests.
set "SPECS=tests/e2e/admin-console.spec.js tests/e2e/user-console.spec.js tests/e2e/setup-wizard.spec.js tests/e2e/responsive.spec.js tests/e2e/user-workflows.spec.js tests/e2e/shared-disk.spec.js tests/e2e/disk-tabs.spec.js"
if /I "%SCOPE%"=="admin" set "SPECS=tests/e2e/admin-console.spec.js tests/e2e/setup-wizard.spec.js"
if /I "%SCOPE%"=="user" set "SPECS=tests/e2e/user-console.spec.js"
if /I "%SCOPE%"=="responsive" set "SPECS=tests/e2e/responsive.spec.js"
if /I "%SCOPE%"=="workflows" set "SPECS=tests/e2e/user-workflows.spec.js"
if /I "%SCOPE%"=="sharedisk" set "SPECS=tests/e2e/shared-disk.spec.js"
if /I "%SCOPE%"=="disktabs" set "SPECS=tests/e2e/disk-tabs.spec.js"

pushd web
echo [test-ui] running click-through tests: %SPECS%
call npx playwright test %SPECS% --reporter=list
set "RESULT=%ERRORLEVEL%"
popd

if not "%RESULT%"=="0" (
    echo [test-ui] click-through tests failed
    exit /b 1
)

echo [test-ui] all click-through tests passed
exit /b 0
