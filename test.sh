#!/usr/bin/env bash
# mudp all-in-one test runner (Linux/macOS counterpart of test.bat)
# Usage: ./test.sh [go|web]
#   go     - run Go backend tests only
#   web    - run web frontend tests only
#   (none) - run both Go and web tests

SCOPE="${1:-}"
FAILED=0

# Anchor to the repo root regardless of the caller's cwd, since every path
# below (go test ./..., web) is relative to it.
cd "$(dirname "$0")" || exit 1

if ! command -v go >/dev/null 2>&1; then
    echo "[test] go is not installed or not in PATH"
    exit 1
fi

if [ "$SCOPE" != "web" ]; then
    echo "[test] running Go tests..."
    if go test ./...; then
        echo "[test] Go tests passed"
    else
        echo "[test] Go tests failed"
        FAILED=1
    fi
fi

if [ "$SCOPE" != "go" ]; then
    if ! command -v npm >/dev/null 2>&1; then
        echo "[test] npm is not installed or not in PATH"
        exit 1
    fi
    echo "[test] running web tests..."
    if (cd web && npm test); then
        echo "[test] web tests passed"
    else
        echo "[test] web tests failed"
        FAILED=1
    fi
fi

if [ "$FAILED" = "1" ]; then
    echo "[test] some tests failed"
    exit 1
fi

echo "[test] all tests passed"
