package dnsmasq

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAdd_ReproValidation_BugConfirmed confirms the pre-fix behaviour:
// Add writes the per-port conf file to disk BEFORE calling reload(), so even
// if reload() returns an error (e.g., dnsmasq not running in CI), the file
// containing the unsanitized ip/name has already been flushed to disk.
//
// After the fix, Add must reject invalid input and must NOT write any file.
//
// Note on "127.0.0.1\r" (trailing CR): strings.TrimSpace strips it, yielding
// the valid IP "127.0.0.1", so the file IS written with clean content — that
// is the correct sanitise-then-validate path and is intentionally not tested
// here.  We instead test an EMBEDDED CR ("127.0.0\r.1") which TrimSpace
// cannot remove and which net.ParseIP correctly rejects.
func TestAdd_ReproValidation_BugConfirmed(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{Dir: dir}

	cases := []struct {
		desc string
		ip   string
		name string
		port int
	}{
		{"newline in ip", "127.0.0.1\n10.0.0.1", "good.test", 8001},
		{"newline in name", "127.0.0.1", "good.test\nevil.test", 8002},
		{"embedded CR in ip", "127.0.0\r.1", "good.test", 8003},
		{"tab in name", "127.0.0.1", "good\tevil", 8004},
		{"non-IP value in ip", "not-an-ip", "good.test", 8005},
		{"shell metachar in name", "127.0.0.1", "good;rm -rf /", 8006},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			// Ignore the return value: reload() fails in CI because dnsmasq is
			// not running, so we cannot use err to detect the bug here.
			_ = m.Add(tc.port, tc.name, tc.ip)

			confFile := filepath.Join(dir, "port-"+itoa(tc.port)+".conf")
			data, readErr := os.ReadFile(confFile)
			if readErr == nil {
				// File exists → validation guard did not fire → bug still present.
				t.Errorf("[BUG CONFIRMED] conf file was written for invalid input "+
					"(ip=%q name=%q):\nfile content: %q",
					tc.ip, tc.name, string(data))
			}
		})
	}
}

// TestAdd_TrimWhitespace verifies that purely leading/trailing whitespace is
// silently stripped and the sanitised values are accepted.
func TestAdd_TrimWhitespace(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{Dir: dir}

	// "  127.0.0.1\r\n" and "  good.test  " → after TrimSpace both are valid.
	// Add will fail at reload() in CI, but the conf file must have been written
	// with the trimmed (clean) content.
	port := 9001
	_ = m.Add(port, "  good.test  ", "  127.0.0.1\r\n")

	confFile := filepath.Join(dir, "port-"+itoa(port)+".conf")
	data, err := os.ReadFile(confFile)
	if err != nil {
		t.Fatalf("conf file was not written after trimming whitespace: %v", err)
	}
	content := string(data)
	if content != "127.0.0.1\tgood.test\n" {
		t.Errorf("unexpected content after trim: %q (want %q)", content, "127.0.0.1\tgood.test\n")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}
