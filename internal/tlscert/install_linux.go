package tlscert

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const linuxCADest = "/usr/local/share/ca-certificates/local-auto-domain-ca.crt"

// InstallCA copies caFile to the system CA store and runs update-ca-certificates.
// Requires sudo.
func InstallCA(caFile string) error {
	data, err := os.ReadFile(caFile)
	if err != nil {
		return err
	}

	cmd := exec.Command("sudo", "tee", linuxCADest)
	cmd.Stdin = strings.NewReader(string(data))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("copying CA to %s: %w\n%s", linuxCADest, err, out)
	}

	if out, err := exec.Command("sudo", "update-ca-certificates").CombinedOutput(); err != nil {
		return fmt.Errorf("update-ca-certificates: %w\n%s", err, out)
	}
	return nil
}

// RemoveCA removes the local-auto-domain CA from the system CA store.
// Requires sudo.
func RemoveCA(_ string) error {
	if out, err := exec.Command("sudo", "rm", "-f", linuxCADest).CombinedOutput(); err != nil {
		return fmt.Errorf("removing CA: %w\n%s", err, out)
	}
	if out, err := exec.Command("sudo", "update-ca-certificates").CombinedOutput(); err != nil {
		return fmt.Errorf("update-ca-certificates: %w\n%s", err, out)
	}
	return nil
}
