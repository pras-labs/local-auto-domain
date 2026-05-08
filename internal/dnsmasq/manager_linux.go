package dnsmasq

import (
	"fmt"
	"os/exec"
)

// reload asks systemd to reload dnsmasq via SIGHUP. Uses sudo with a
// NOPASSWD rule written by lad setup so no interactive prompt is needed.
// systemctl reload is preferred over pkill: systemd tracks the correct PID
// even if dnsmasq drops privileges to nobody after start.
func (m *Manager) reload() error {
	if err := exec.Command("sudo", "systemctl", "reload", "dnsmasq").Run(); err != nil {
		return fmt.Errorf("reloading dnsmasq: %w", err)
	}
	return nil
}
