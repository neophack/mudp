package server

import "os/exec"

// powershellCommand runs script through Windows PowerShell with its stdout
// switched to UTF-8. Piped output otherwise uses the console's OEM code page
// (GBK on Chinese Windows), which garbles non-ASCII volume labels, paths and
// error messages once read back as UTF-8.
func powershellCommand(script string) *exec.Cmd {
	return exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; "+script) // #nosec G204 -- callers escape user input with escapePathForPowerShell
}
