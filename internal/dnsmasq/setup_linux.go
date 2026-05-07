package dnsmasq

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const linuxDropInDir = "/etc/dnsmasq.d/local-auto-domain"

func DropInDir() string { return linuxDropInDir }

// Setup performs first-run dnsmasq configuration on Linux.
// Requires root or sudo privileges.
func Setup() error {
	// 1. Install dnsmasq if missing
	if _, err := exec.LookPath("dnsmasq"); err != nil {
		fmt.Println("Installing dnsmasq...")
		if err := installDnsmasq(); err != nil {
			return err
		}
	}

	// 2. Create drop-in dir
	if out, err := exec.Command("sudo", "mkdir", "-p", linuxDropInDir).CombinedOutput(); err != nil {
		return fmt.Errorf("creating drop-in dir: %w\n%s", err, out)
	}
	// Make it writable by current user so daemon doesn't need sudo at runtime
	user := os.Getenv("USER")
	if user != "" {
		exec.Command("sudo", "chown", user, linuxDropInDir).Run()
	}

	// 3. Ensure main dnsmasq.conf includes our drop-in dir
	confLine := fmt.Sprintf("conf-dir=%s,*.conf", linuxDropInDir)
	if err := ensureConf(confLine); err != nil {
		return err
	}

	// 4. Configure systemd-resolved split-DNS if active
	configureSplitDNS()

	// 5. Enable + start dnsmasq
	fmt.Println("Enabling dnsmasq...")
	exec.Command("sudo", "systemctl", "enable", "--now", "dnsmasq").Run()
	exec.Command("sudo", "systemctl", "restart", "dnsmasq").Run()

	fmt.Println("dnsmasq configured. Domains under .tunnel.test will resolve to 127.0.0.1")
	return nil
}

// Teardown reverses Setup: removes systemd-resolved config, drop-in dir, and the
// conf-dir line from /etc/dnsmasq.conf. Requires sudo for system files.
func Teardown() error {
	const resolvedDropIn = "/etc/systemd/resolved.conf.d/local-auto-domain.conf"

	// Remove systemd-resolved drop-in + restart
	exec.Command("sudo", "rm", "-f", resolvedDropIn).Run()
	exec.Command("sudo", "systemctl", "restart", "systemd-resolved").Run()

	// Remove drop-in dir (was created with sudo, needs sudo to remove)
	exec.Command("sudo", "rm", "-rf", linuxDropInDir).Run()

	// Remove conf-dir line from /etc/dnsmasq.conf via sudo tee
	confLine := fmt.Sprintf("conf-dir=%s,*.conf", linuxDropInDir)
	removeLineFromFileWithSudo("/etc/dnsmasq.conf", confLine)

	// Restart dnsmasq so it picks up the removed config
	exec.Command("sudo", "systemctl", "restart", "dnsmasq").Run()

	fmt.Println("dnsmasq configuration removed.")
	return nil
}

func removeLineFromFileWithSudo(path, line string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var kept []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != strings.TrimSpace(line) {
			kept = append(kept, l)
		}
	}
	cmd := exec.Command("sudo", "tee", path)
	cmd.Stdin = strings.NewReader(strings.Join(kept, "\n"))
	cmd.Run()
}

func installDnsmasq() error {
	managers := [][]string{
		{"apt-get", "-y", "install", "dnsmasq"},
		{"dnf", "-y", "install", "dnsmasq"},
		{"pacman", "-S", "--noconfirm", "dnsmasq"},
	}
	for _, args := range managers {
		if _, err := exec.LookPath(args[0]); err == nil {
			out, err := exec.Command("sudo", append([]string{args[0]}, args[1:]...)...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("%s install: %w\n%s", args[0], err, out)
			}
			return nil
		}
	}
	return fmt.Errorf("no supported package manager found; install dnsmasq manually")
}

func ensureConf(line string) error {
	const path = "/etc/dnsmasq.conf"
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(data), line) {
		return nil
	}
	cmd := exec.Command("sudo", "tee", "-a", path)
	cmd.Stdin = strings.NewReader("\n" + line + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("updating dnsmasq.conf: %w\n%s", err, out)
	}
	return nil
}

// configureSplitDNS sets up systemd-resolved to forward .tunnel.test queries to 127.0.0.1.
func configureSplitDNS() {
	const resolvedDropIn = "/etc/systemd/resolved.conf.d/local-auto-domain.conf"
	const content = "[Resolve]\nDNS=127.0.0.1\nDomains=~tunnel.test\n"

	exec.Command("sudo", "mkdir", "-p", "/etc/systemd/resolved.conf.d").Run()
	cmd := exec.Command("sudo", "tee", resolvedDropIn)
	cmd.Stdin = strings.NewReader(content)
	if err := cmd.Run(); err == nil {
		exec.Command("sudo", "systemctl", "restart", "systemd-resolved").Run()
	}
}
