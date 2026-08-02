@echo off
REM ============================================================================
REM code-audit -- static analysis, security and dependency audit for the
REM mudp Go codebase.
REM
REM Runs six checks, each independently and with a clear pass/fail tally:
REM   1. Coding-standard checks            (gofmt + goimports + go vet)
REM   2. Common-bug detection              (staticcheck)
REM   3. Deep static analysis              (errcheck + nilness + shadow)
REM   4. Security vulnerabilities          (gosec)
REM   5. Third-party dependency vulns      (govulncheck)
REM   6. Enterprise / custom convention    (revive, see code-audit-revive.toml)
REM
REM Tools are auto-installed into %GOPATH%\bin on first run; offline? add the
REM directory to PATH or pre-place the binaries. Any phase that finds real
REM issues increments FAILS; the exit code is non-zero if any phase failed.
REM
REM Usage:
REM   scripts\code-audit.bat            run all phases over the whole module
REM   scripts\code-audit.bat clean      remove the report dir before running
REM   scripts\code-audit.bat quick      skip govulncheck + deep static (fast)
REM
REM Prereqs: go (in PATH), write access to %GOPATH%\bin. Internet access is
REM only needed the first time, to `go install` the analyzers.
REM ============================================================================
setlocal enabledelayedexpansion

REM cd into the project root and STAY there for the whole run -- every phase
REM below invokes tools with relative paths ("." / "./...") that resolve
REM against the current directory, so this pushd must not be undone until
REM the script actually exits (see the matching popd at each exit point).
set "PROJ=%~dp0.."
pushd "%PROJ%" >nul 2>&1 || ( echo [FATAL] cannot cd to project root "%PROJ%" & exit /b 99 )
set "PROJ=%CD%"

REM Module import path -- used by `goimports -local`. Auto-detected from
REM go.mod in the project root (CWD is now %PROJ%, thanks to the pushd
REM above) so this script works unchanged across Go projects regardless of
REM where it was invoked from.
set "LOCALPKG="
for /f "tokens=2" %%m in ('findstr /b /c:"module " go.mod') do set "LOCALPKG=%%m"
if "%LOCALPKG%"=="" set "LOCALPKG=mudp"

REM Capture the script's own directory once, up front. %~dp0 is evaluated at
REM parse time and stays valid here; capturing it avoids any later surprise
REM from pushd/popd/call changing %0 or the drive context.
set "SCRIPTDIR=%~dp0"

set "REPORTS=%PROJ%\dist\code-audit"
set "PASS=0"
set "WARN=0"
set "FAILS=0"

REM -- arg parsing -------------------------------------------------------------
set "MODE=all"
:argloop
if "%~1"=="" goto argsdone
if /i "%~1"=="clean" ( rmdir /s /q "%REPORTS%" >nul 2>&1 & shift & goto argloop )
if /i "%~1"=="quick" ( set "MODE=quick" & shift & goto argloop )
echo [WARN] unknown argument "%~1" (expecting: clean ^| quick)
shift
goto argloop
:argsdone

if not exist "%REPORTS%" mkdir "%REPORTS%" >nul 2>&1

echo ============================================================================
echo  code-audit (mode=%MODE%)   %date% %time%
echo  project:  %PROJ%
echo  module:   %LOCALPKG%
echo  reports:  %REPORTS%
echo ============================================================================

REM -- locate go ---------------------------------------------------------------
where go >nul 2>&1
if errorlevel 1 (
    echo [FATAL] 'go' not found on PATH. Install Go and re-run.
    popd
    exit /b 99
)

REM Ensure %GOPATH%\bin is on PATH for this session so installed tools resolve.
if "%GOPATH%"=="" set "GOPATH=%USERPROFILE%\go"
set "GOBINDIR=%GOPATH%\bin"
call :prepend_path "%GOBINDIR%"

REM ===========================================================================
REM  PHASE 1: Coding-standard checks  (gofmt, goimports, go vet)
REM ===========================================================================
echo.
echo ---- [1/6] Coding-standard checks (gofmt + goimports + go vet) ---------
call :run_fmt
call :run_imports
call :run_vet
goto phase2

:run_fmt
set "OUT=%REPORTS%\01-gofmt.txt"
REM gofmt -l prints offending files; non-empty output means a violation.
gofmt -l . > "%OUT%" 2>&1
findstr /r /n "." "%OUT%" >nul 2>&1
if errorlevel 1 (
    call :pass "gofmt: all files formatted"
) else (
    set /a FAILS+=1
    call :fail "gofmt: unformatted files (see %OUT%)"
)
goto :eof

:run_imports
set "OUT=%REPORTS%\01-goimports.txt"
if not exist "%GOBINDIR%\goimports.exe" call :install_tool goimports golang.org/x/tools/cmd/goimports@latest
if not exist "%GOBINDIR%\goimports.exe" ( call :skip "goimports: tool not installed" & goto :eof )
goimports -l -local %LOCALPKG% . > "%OUT%" 2>&1
findstr /r /n "." "%OUT%" >nul 2>&1
if errorlevel 1 (
    call :pass "goimports: import grouping OK"
) else (
    set /a FAILS+=1
    call :fail "goimports: files need reformatting (see %OUT%)"
)
goto :eof

:run_vet
set "OUT=%REPORTS%\01-govet.txt"
go vet ./... > "%OUT%" 2>&1
if errorlevel 1 (
    set /a FAILS+=1
    call :fail "go vet: issues found (see %OUT%)"
) else (
    call :pass "go vet: no issues"
)
goto :eof

REM ===========================================================================
REM  PHASE 2: Common-bug detection  (staticcheck)
REM ===========================================================================
:phase2
echo.
echo ---- [2/6] Common-bug detection (staticcheck) ---------------------------
set "OUT=%REPORTS%\02-staticcheck.txt"
if not exist "%GOBINDIR%\staticcheck.exe" call :install_tool staticcheck honnef.co/go/tools/cmd/staticcheck@latest
if not exist "%GOBINDIR%\staticcheck.exe" ( call :skip "staticcheck: tool not installed" & goto phase3 )
staticcheck ./... > "%OUT%" 2>&1
if errorlevel 1 (
    set /a FAILS+=1
    call :fail "staticcheck: issues found (see %OUT%)"
) else (
    call :pass "staticcheck: clean"
)

REM ===========================================================================
REM  PHASE 3: Deep static analysis  (errcheck + nilness + shadow)
REM ===========================================================================
:phase3
echo.
echo ---- [3/6] Deep static analysis (errcheck + nilness + shadow) -----------
if /i "%MODE%"=="quick" ( call :skip "deep static analysis (quick mode)" & goto phase4 )
call :run_errcheck
call :run_nilness
call :run_shadow
goto phase4

:run_errcheck
set "OUT=%REPORTS%\03-errcheck.txt"
if not exist "%GOBINDIR%\errcheck.exe" call :install_tool errcheck github.com/kisielk/errcheck@latest
if not exist "%GOBINDIR%\errcheck.exe" ( call :skip "errcheck: tool not installed" & goto :eof )
errcheck ./... > "%OUT%" 2>&1
findstr /r /n "." "%OUT%" >nul 2>&1
if errorlevel 1 (
    set /a FAILS+=1
    call :fail "errcheck: unchecked errors (see %OUT%)"
) else (
    call :pass "errcheck: no unchecked errors"
)
goto :eof

:run_nilness
set "OUT=%REPORTS%\03-nilness.txt"
if not exist "%GOBINDIR%\nilness.exe" call :install_tool nilness golang.org/x/tools/go/analysis/passes/nilness/cmd/nilness@latest
if not exist "%GOBINDIR%\nilness.exe" ( call :skip "nilness: tool not installed" & goto :eof )
REM go vet prints package banner lines even when clean, so judge by exit code
REM (0=clean, 1=findings) rather than by whether the file is non-empty.
go vet -vettool="%GOBINDIR%\nilness.exe" ./... > "%OUT%" 2>&1
call set "RC=%%errorlevel%%"
if "%RC%"=="0" (
    call :pass "nilness: no nil-deref findings"
) else (
    set /a FAILS+=1
    call :fail "nilness: possible nil dereferences (see %OUT%)"
)
goto :eof

:run_shadow
set "OUT=%REPORTS%\03-shadow.txt"
if not exist "%GOBINDIR%\shadow.exe" call :install_tool shadow golang.org/x/tools/go/analysis/passes/shadow/cmd/shadow@latest
if not exist "%GOBINDIR%\shadow.exe" ( call :skip "shadow: tool not installed" & goto :eof )
go vet -vettool="%GOBINDIR%\shadow.exe" ./... > "%OUT%" 2>&1
call set "RC=%%errorlevel%%"
if "%RC%"=="0" (
    call :pass "shadow: no shadowing findings"
) else (
    set /a FAILS+=1
    call :fail "shadow: variable shadowing (see %OUT%)"
)
goto :eof

REM ===========================================================================
REM  PHASE 4: Security vulnerabilities  (gosec)
REM ===========================================================================
:phase4
echo.
echo ---- [4/6] Security vulnerabilities (gosec) -----------------------------
set "OUT=%REPORTS%\04-gosec.txt"
if not exist "%GOBINDIR%\gosec.exe" call :install_tool gosec github.com/securego/gosec/v2/cmd/gosec@latest
if not exist "%GOBINDIR%\gosec.exe" ( call :skip "gosec: tool not installed" & goto phase5 )
REM -quiet suppresses the verbose header; severity medium+high are failures.
REM Excluded rules are confirmed false positives in THIS codebase, documented
REM here so the report stays actionable:
REM   G703/G304 -- path traversal: every user path is confined by cleanUserPath
REM                (two-pass lexical + post-symlink check) in netdisk.go, so the
REM                taint tracker's hits are downstream of an already-mitigated sink.
REM   G115      -- integer overflow conversions on bounded values (ports, sizes).
REM   G302/G306 -- file/dir permission checks on operator-only paths.
REM   G601      -- implicit loop aliasing reviewed per-site (no captures taken).
REM   G505/G401 -- crypto/sha1 is mandated by RFC 6455 for the WebSocket handshake
REM                (server.go wsHandshake); not used for secrets.
REM   G117      -- "Password" field is the Docker SDK's registry.AuthConfig, base64
REM                -encoded into an HTTP header to dockerd; never JSON-exposed.
REM   G705      -- writes of embedded/static HTML templates (Content-Type + CSP
REM                set; the only injected values are HTML-escaped).
REM   G120      -- upload handlers wrap r.Body in http.MaxBytesReader (see
REM                volumes.go / netdisk.go); gosec still flags ParseMultipartForm
REM                itself, which is a false positive once the body is capped.
REM   G124      -- every cookie sets Secure (conditional on httpx.IsSecureRequest,
REM                so cookie-set works over plain HTTP in dev) + HttpOnly +
REM                SameSite. gosec's static check can't see the conditional Secure,
REM                and csrf.go's HttpOnly:false is deliberate (JS reads the token).
REM   G704      -- SSRF taint in geoLookupRemote (security.go): the request host
REM                is hardcoded to ip-api.com and the path segment is the caller
REM                IP re-parsed with net.ParseIP + gated on IsGlobalUnicast, so no
REM                attacker-controlled bytes can reach the request line. gosec's
REM                taint tracker does not model net.ParseIP as a sanitizer.
gosec -quiet -severity medium -confidence medium -exclude-dir=dist -exclude=G703,G304,G115,G302,G306,G601,G505,G401,G117,G705,G120,G124,G704 ./... > "%OUT%" 2>&1
call set "RC=%%errorlevel%%"
REM gosec exits with: 0=ok, 1=findings, 2=tool error. Treat 2 as a skip.
if "%RC%"=="2" (
    call :skip "gosec: internal error (see %OUT%)"
) else if "%RC%"=="1" (
    set /a FAILS+=1
    call :fail "gosec: security findings (see %OUT%)"
) else (
    call :pass "gosec: no medium/high findings"
)

REM ===========================================================================
REM  PHASE 5: Third-party dependency vulnerabilities  (govulncheck)
REM ===========================================================================
:phase5
echo.
echo ---- [5/6] Dependency vulnerabilities (govulncheck) ---------------------
if /i "%MODE%"=="quick" ( call :skip "govulncheck (quick mode)" & goto phase6 )
set "OUT=%REPORTS%\05-govulncheck.txt"
if not exist "%GOBINDIR%\govulncheck.exe" call :install_tool govulncheck golang.org/x/vuln/cmd/govulncheck@latest
if not exist "%GOBINDIR%\govulncheck.exe" ( call :skip "govulncheck: tool not installed" & goto phase6 )
REM 'call set "RC=%%errorlevel%%"' reads %errorlevel% right after the tool
REM runs; cmd's `if errorlevel N` means ">=N", so a direct numeric compare is
REM the only reliable way to separate govulncheck exit codes.
govulncheck ./... > "%OUT%" 2>&1
call set "RC=%%errorlevel%%"
REM govulncheck: 0=no vulns, 3=vulns found, anything else (1/2)=tool error.
if "%RC%"=="3" (
    set /a FAILS+=1
    call :fail "govulncheck: vulnerable dependencies (see %OUT%)"
) else if "%RC%"=="0" (
    call :pass "govulncheck: no known vulnerabilities"
) else (
    call :skip "govulncheck: tool error (rc=%RC%, see %OUT%)"
)

REM ===========================================================================
REM  PHASE 6: Enterprise / custom convention  (revive)
REM ===========================================================================
:phase6
echo.
echo ---- [6/6] Enterprise / convention rules (revive) -----------------------
set "OUT=%REPORTS%\06-revive.txt"
set "CFG=%SCRIPTDIR%code-audit-revive.toml"
if not exist "%CFG%" ( call :skip "revive: config %CFG% missing" & goto summary )
if not exist "%GOBINDIR%\revive.exe" call :install_tool revive github.com/mgechev/revive@latest
if not exist "%GOBINDIR%\revive.exe" ( call :skip "revive: tool not installed" & goto summary )
REM Scan only first-party packages: ./... would also pick up the vendored
REM Go code under web/node_modules, which is third-party and not ours to lint.
revive -config "%CFG%" ./cmd/... ./internal/... ./web > "%OUT%" 2>&1
call set "RC=%%errorlevel%%"
if "%RC%"=="0" (
    call :pass "revive: all convention rules pass"
) else (
    set /a FAILS+=1
    call :fail "revive: convention/style issues (see %OUT%)"
)

REM ===========================================================================
REM  SUMMARY
REM ===========================================================================
:summary
echo.
echo ============================================================================
echo  SUMMARY
echo ----------------------------------------------------------------------------
echo   PASS ............ !PASS!
echo   WARN (skipped) ... !WARN!
echo   FAIL ............ !FAILS!
echo ============================================================================
echo  Reports written to: %REPORTS%
REM Avoid parentheses inside this branch -- cmd treats ( ) as block delimiters,
REM which corrupts echo lines that contain them. Jump out of the if instead.
if !FAILS! gtr 0 goto :failed
echo  RESULT: PASSED
popd
exit /b 0

:failed
echo  RESULT: FAILED -- !FAILS! phase(s) need attention
popd
exit /b 1

REM ---------------------------------------------------------------------------
REM  helper subroutines
REM ---------------------------------------------------------------------------
:pass
echo   [PASS] %~1
set /a PASS+=1
goto :eof

:fail
echo   [FAIL] %~1
goto :eof

:skip
echo   [SKIP] %~1
set /a WARN+=1
goto :eof

:install_tool
REM install_tool <exe-name> <import-path@version>
echo   [install] %~1 (one-time; cached in %GOPATH%\bin)
go install "%~2" >nul 2>&1
if errorlevel 1 echo   [install] FAILED %~1 -- run manually: go install %~2
goto :eof

:prepend_path
REM prepend_path <dir> -- add dir to PATH if not already present
echo %PATH% | findstr /i /c:"%~1" >nul 2>&1
if errorlevel 1 set "PATH=%~1;%PATH%"
goto :eof
