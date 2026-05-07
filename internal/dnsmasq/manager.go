package dnsmasq

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Manager writes per-domain dnsmasq config files and reloads dnsmasq via SIGHUP.
type Manager struct {
	Dir string
}

func New(dir string) *Manager {
	return &Manager{Dir: dir}
}

// Add writes address=/name/ip config and reloads dnsmasq.
func (m *Manager) Add(port int, name, ip string) error {
	if err := os.MkdirAll(m.Dir, 0755); err != nil {
		return err
	}
	content := fmt.Sprintf("address=/%s/%s\n", name, ip)
	if err := os.WriteFile(m.confPath(port), []byte(content), 0644); err != nil {
		return err
	}
	return m.reload()
}

// Remove deletes the config file for a port and reloads dnsmasq.
func (m *Manager) Remove(port int) error {
	if err := os.Remove(m.confPath(port)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return m.reload()
}

// RemoveAll deletes all managed config files.
func (m *Manager) RemoveAll() error {
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".conf" {
			os.Remove(filepath.Join(m.Dir, e.Name()))
		}
	}
	return m.reload()
}

func (m *Manager) confPath(port int) string {
	return filepath.Join(m.Dir, fmt.Sprintf("port-%d.conf", port))
}

func (m *Manager) reload() error {
	exec.Command("pkill", "-HUP", "dnsmasq").Run()
	return nil
}
