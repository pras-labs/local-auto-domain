package dnsmasq

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Manager writes per-domain state files and maintains a single addn-hosts file
// that dnsmasq re-reads on SIGHUP. dnsmasq does NOT re-read conf-dir files on
// SIGHUP — only addn-hosts files are reloaded dynamically.
type Manager struct {
	Dir string
}

// validHostname matches safe hostname values: starts with a letter or digit,
// followed by zero or more letters, digits, hyphens, or dots. This rejects
// any string containing whitespace, control characters, or shell metacharacters.
var validHostname = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.\-]*$`)

func New(dir string) *Manager {
	return &Manager{Dir: dir}
}

// Add writes the state file for port and updates the addn-hosts file, then reloads dnsmasq.
func (m *Manager) Add(port int, name, ip string) error {
	// Trim surrounding whitespace first so that a lone "\n" or " " is caught
	// by the subsequent validators rather than slipping through.
	ip = strings.TrimSpace(ip)
	name = strings.TrimSpace(name)

	// Validate ip: net.ParseIP accepts only well-formed IPv4/IPv6 addresses and
	// rejects any string containing control characters or extra text.
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("dnsmasq manager: invalid IP address %q", ip)
	}

	// Validate name: only letters, digits, hyphens, and dots are permitted.
	// This prevents newline/tab/control-character injection and rejects shell
	// metacharacters that could corrupt the hosts file.
	if !validHostname.MatchString(name) {
		return fmt.Errorf("dnsmasq manager: invalid hostname %q (only letters, digits, hyphens, and dots are allowed)", name)
	}

	if err := os.MkdirAll(m.Dir, 0755); err != nil {
		return err
	}
	content := fmt.Sprintf("%s\t%s\n", ip, name)
	if err := os.WriteFile(m.confPath(port), []byte(content), 0644); err != nil {
		return err
	}
	if err := m.writeHostsFile(); err != nil {
		return err
	}
	return m.reload()
}

// Remove deletes the state file for port and updates the addn-hosts file, then reloads dnsmasq.
func (m *Manager) Remove(port int) error {
	if err := os.Remove(m.confPath(port)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := m.writeHostsFile(); err != nil {
		return err
	}
	return m.reload()
}

// RemoveAll deletes all port state files and clears the addn-hosts file.
func (m *Manager) RemoveAll() error {
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		// Only remove port-*.conf state files; leave 00-port.conf (startup config).
		if strings.HasPrefix(e.Name(), "port-") && filepath.Ext(e.Name()) == ".conf" {
			os.Remove(filepath.Join(m.Dir, e.Name()))
		}
	}
	if err := m.writeHostsFile(); err != nil {
		return err
	}
	return m.reload()
}

// writeHostsFile rebuilds the addn-hosts file from all active port state files.
// dnsmasq re-reads this file on SIGHUP, making new domains immediately resolvable.
func (m *Manager) writeHostsFile() error {
	entries, _ := os.ReadDir(m.Dir)
	var lines []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "port-") || filepath.Ext(e.Name()) != ".conf" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.Dir, e.Name()))
		if err != nil {
			continue
		}
		line := strings.TrimSpace(string(data))
		if line != "" {
			lines = append(lines, line)
		}
	}
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	return os.WriteFile(m.hostsPath(), []byte(content), 0644)
}

func (m *Manager) hostsPath() string {
	return filepath.Join(m.Dir, "hosts")
}

func (m *Manager) confPath(port int) string {
	return filepath.Join(m.Dir, fmt.Sprintf("port-%d.conf", port))
}

// reload is implemented per-platform in manager_darwin.go and manager_linux.go.
