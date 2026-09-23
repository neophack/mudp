package server

import "testing"

// Non-ASCII output (a Chinese volume label, path or error) must come back as
// UTF-8 regardless of the console code page the service inherits.
func TestPowershellCommandOutputsUTF8(t *testing.T) {
	out, err := powershellCommand("Write-Output '中文卷'").Output()
	if err != nil {
		t.Skipf("powershell unavailable: %v", err)
	}
	if got := string(bytesTrimBOM(out)); got != "中文卷\r\n" {
		t.Fatalf("output = %q (% x), want UTF-8 中文卷", got, out)
	}
}
