package netutil

import (
	"fmt"
	"net"
	"os/exec"
)

// EnsureAlias adds a loopback alias for ip on lo0 if not already present.
// The alias should already exist (created during `setup`), so this is just a safety net.
func EnsureAlias(ip net.IP) error {
	out, err := exec.Command("ifconfig", "lo0", "alias", ip.String()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ifconfig lo0 alias %s: %w\n%s", ip, err, out)
	}
	return nil
}

// RemoveAlias removes the loopback alias for ip from lo0.
func RemoveAlias(ip net.IP) error {
	exec.Command("ifconfig", "lo0", "-alias", ip.String()).Run()
	return nil
}
