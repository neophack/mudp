package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// CreateSymlink creates a symlink (on Linux) or shortcut file (on Windows) named
// after displayName, pointing to the target directory. Both are created in parentDir.
// On Windows, the shortcut is a .lnk file. On Linux, it's a symbolic link.
func CreateSymlink(parentDir, targetPath, displayName string) error {
	if displayName == "" {
		return fmt.Errorf("displayName cannot be empty")
	}
	// displayName can come from an untrusted source (a Feishu OAuth profile
	// name). It becomes a filename joined under parentDir and, on Windows, is
	// embedded in a PowerShell script — reject path separators and traversal
	// so it can neither escape parentDir nor break out of that script.
	if strings.ContainsAny(displayName, `/\`) || displayName == "." || displayName == ".." {
		return fmt.Errorf("displayName contains invalid characters")
	}

	targetPath = filepath.Clean(targetPath)
	info, err := os.Stat(targetPath)
	if err != nil {
		return fmt.Errorf("target path does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("target path is not a directory")
	}

	if runtime.GOOS == "windows" {
		return createWindowsShortcut(parentDir, targetPath, displayName)
	}
	return createLinuxSymlink(parentDir, targetPath, displayName)
}

// createLinuxSymlink creates a symbolic link on Linux systems.
func createLinuxSymlink(parentDir, targetPath, displayName string) error {
	linkPath := filepath.Join(parentDir, displayName)

	// Clean up any existing link
	if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove existing symlink: %w", err)
	}

	// Convert to absolute path if target is relative
	absTarget := targetPath
	if !filepath.IsAbs(absTarget) {
		var err error
		absTarget, err = filepath.Abs(absTarget)
		if err != nil {
			return fmt.Errorf("failed to convert to absolute path: %w", err)
		}
	}

	if err := os.Symlink(absTarget, linkPath); err != nil {
		return fmt.Errorf("failed to create symlink: %w", err)
	}
	return nil
}

// createWindowsShortcut creates a Windows shortcut (.lnk file) using PowerShell.
// The shortcut is named after displayName (with .lnk extension if not present).
func createWindowsShortcut(parentDir, targetPath, displayName string) error {
	if !strings.HasSuffix(displayName, ".lnk") {
		displayName = displayName + ".lnk"
	}
	linkPath := filepath.Join(parentDir, displayName)

	// Clean up any existing shortcut
	if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove existing shortcut: %w", err)
	}

	// Get absolute path
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Escape paths for PowerShell
	linkPathEscaped := escapePathForPowerShell(linkPath)
	targetPathEscaped := escapePathForPowerShell(absTarget)

	// PowerShell script to create a shortcut
	// This creates a .lnk file that points to the target directory
	//
	// linkPathEscaped/targetPathEscaped are already single-quoted PowerShell
	// string literals (see escapePathForPowerShell) — they must be embedded
	// as-is, NOT re-wrapped in double quotes. A double-quoted PS string still
	// interpolates $(...), backticks, and embedded ", so wrapping an
	// attacker-influenced value in "%s" would let it break out of the string
	// and inject arbitrary PowerShell.
	psScript := fmt.Sprintf(`
$WshShell = New-Object -ComObject WScript.Shell
$Shortcut = $WshShell.CreateShortcut(%s)
$Shortcut.TargetPath = %s
$Shortcut.WorkingDirectory = %s
$Shortcut.Save()
`, linkPathEscaped, targetPathEscaped, targetPathEscaped)

	// Execute PowerShell script. psScript is built only from
	// linkPathEscaped/targetPathEscaped, which ran through escapePathForPowerShell
	// (single-quote wrapping + ' escaping); single quotes suppress PowerShell's
	// $()/backtick interpolation, and displayName is rejected if it contains
	// / \ . .. upstream (symlink.go:23-25).
	cmd := exec.Command("powershell", "-NoProfile", "-Command", psScript) // #nosec G204,G702 -- see rationale above
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create shortcut: %w (output: %s)", err, string(output))
	}

	return nil
}

// escapePathForPowerShell escapes a file path for use in PowerShell strings.
// It wraps the path in single quotes and escapes any single quotes inside.
func escapePathForPowerShell(path string) string {
	// Escape single quotes by replacing ' with ''
	escaped := strings.ReplaceAll(path, "'", "''")
	return "'" + escaped + "'"
}

// EnsureUserNetdiskDir creates the user's netdisk directory and a shortcut/symlink
// to it using the user's display name. The symlink/shortcut is recreated if it is
// missing, so calling it on every login ensures shortcuts stay in sync.
func EnsureUserNetdiskDir(parentDir, username, userID, displayName string) error {
	if parentDir == "" || username == "" {
		return nil // Netdisk not configured or user lacks identifying info
	}

	// Construct the user's directory name the same way netdisk access does.
	dirName := fmt.Sprintf("%s-%s", sanitizePathPart(username), userID)
	userDir := filepath.Join(parentDir, dirName)

	// Create the user directory if it doesn't exist
	if err := os.MkdirAll(userDir, 0750); err != nil {
		return fmt.Errorf("failed to create user netdisk directory: %w", err)
	}

	// Create symlink/shortcut with display name
	if displayName != "" {
		if err := CreateSymlink(parentDir, userDir, displayName); err != nil {
			// Log but don't fail the entire operation if symlink creation fails
			// This prevents user creation from failing due to symlink issues
			fmt.Printf("Warning: failed to create symlink for user %s: %v\n", displayName, err)
		}
	}

	return nil
}
