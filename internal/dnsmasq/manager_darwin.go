package dnsmasq

import (
	"fmt"
	"os/exec"
)

// reload sends SIGHUP to dnsmasq via pkill. No sudo needed: lad setup starts
// dnsmasq as a user-level LaunchAgent (not a root LaunchDaemon) so the daemon
// can signal it directly.
func (m *Manager) reload() error {
	if err := exec.Command("pkill", "-HUP", "dnsmasq").Run(); err != nil {
		return fmt.Errorf("sending SIGHUP to dnsmasq: %w", err)
	}
	return nil
}
