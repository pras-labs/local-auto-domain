package tlscert

import (
	"fmt"
	"os/exec"
	"strings"
)

// InstallCA adds caFile to the macOS system keychain as a trusted root CA.
// Requires sudo.
func InstallCA(caFile string) error {
	out, err := exec.Command("sudo", "security", "add-trusted-cert",
		"-d", "-r", "trustRoot",
		"-k", "/Library/Keychains/System.keychain",
		caFile).CombinedOutput()
	if err != nil {
		return fmt.Errorf("security add-trusted-cert: %w\n%s", err, out)
	}
	return nil
}

// RemoveCA deletes the local-auto-domain CA from the macOS system keychain.
// Requires sudo.
func RemoveCA(_ string) error {
	out, err := exec.Command("sudo", "security", "delete-certificate",
		"-c", "local-auto-domain CA",
		"/Library/Keychains/System.keychain").CombinedOutput()
	if err != nil && !strings.Contains(string(out), "no matching") {
		return fmt.Errorf("security delete-certificate: %w\n%s", err, out)
	}
	return nil
}
