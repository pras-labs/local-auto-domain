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

	// 3. Ensure main dnsmasq.conf includes our drop-in dir and addn-hosts file.
	// addn-hosts is the key directive: dnsmasq re-reads it on SIGHUP, making
	// domain updates take effect without restarting dnsmasq.
	//
	// listen-address + bind-interfaces together prevent dnsmasq from claiming
	// 127.0.0.53:53, which systemd-resolved's stub listener already owns.
	// listen-address alone is insufficient: without bind-interfaces dnsmasq
	// still opens a wildcard socket and filters by address, which can still
	// conflict. bind-interfaces forces it to bind only the listed address.
	if err := ensureConf("listen-address=127.0.0.1"); err != nil {
		return err
	}
	if err := ensureConf("bind-interfaces"); err != nil {
		return err
	}
	confLine := fmt.Sprintf("conf-dir=%s,*.conf", linuxDropInDir)
	if err := ensureConf(confLine); err != nil {
		return err
	}
	hostsFile := linuxDropInDir + "/hosts"
	if err := ensureConf(fmt.Sprintf("addn-hosts=%s", hostsFile)); err != nil {
		return err
	}
	// Create empty hosts file so dnsmasq doesn't log a warning at startup.
	if _, statErr := os.Stat(hostsFile); os.IsNotExist(statErr) {
		os.WriteFile(hostsFile, []byte{}, 0644) //nolint:errcheck
	}

	// 4. Configure systemd-resolved split-DNS if active
	configureSplitDNS()

	// 5. Enable + start dnsmasq
	fmt.Println("Enabling dnsmasq...")
	exec.Command("sudo", "systemctl", "enable", "--now", "dnsmasq").Run()
	exec.Command("sudo", "systemctl", "restart", "dnsmasq").Run()

	// 6. Write sudoers rule so the lad daemon can reload dnsmasq without a
	// password prompt. dnsmasq runs as root/nobody on Linux (must bind port 53),
	// so direct SIGHUP from a user process is blocked. systemctl reload is used
	// instead of pkill: systemd tracks the correct PID even after privilege drop.
	if err := writeSudoers(); err != nil {
		fmt.Printf("Warning: could not write sudoers rule: %v\n", err)
		fmt.Println("dnsmasq reload may fail at runtime; run 'sudo lad setup' to retry.")
	}

	fmt.Println("dnsmasq configured. Domains under .tunnel.test will resolve to 127.0.0.1")
	return nil
}

// Teardown reverses Setup: removes systemd-resolved config, drop-in dir,
// sudoers rule, and the conf-dir line from /etc/dnsmasq.conf.
// Requires sudo for system files.
func Teardown() error {
	const resolvedDropIn = "/etc/systemd/resolved.conf.d/local-auto-domain.conf"

	// Remove systemd-resolved drop-in + restart
	exec.Command("sudo", "rm", "-f", resolvedDropIn).Run()
	exec.Command("sudo", "systemctl", "restart", "systemd-resolved").Run()

	// Remove drop-in dir (was created with sudo, needs sudo to remove)
	exec.Command("sudo", "rm", "-rf", linuxDropInDir).Run()

	// Remove conf-dir, addn-hosts, and listen-address lines from /etc/dnsmasq.conf
	confLine := fmt.Sprintf("conf-dir=%s,*.conf", linuxDropInDir)
	removeLineFromFileWithSudo("/etc/dnsmasq.conf", confLine)
	removeLineFromFileWithSudo("/etc/dnsmasq.conf", fmt.Sprintf("addn-hosts=%s/hosts", linuxDropInDir))
	removeLineFromFileWithSudo("/etc/dnsmasq.conf", "listen-address=127.0.0.1")
	removeLineFromFileWithSudo("/etc/dnsmasq.conf", "bind-interfaces")

	// Remove sudoers rule
	exec.Command("sudo", "rm", "-f", sudoersPath).Run()

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

const sudoersPath = "/etc/sudoers.d/local-auto-domain"

// writeSudoers writes a NOPASSWD rule allowing the current user to run
// "sudo systemctl reload dnsmasq" without a password prompt. This is needed
// because dnsmasq on Linux runs as root (must bind port 53) and drops to
// nobody, so a user-space daemon cannot send it SIGHUP directly.
func writeSudoers() error {
	user := os.Getenv("SUDO_USER")
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" || user == "root" {
		return fmt.Errorf("could not determine non-root user for sudoers rule")
	}

	systemctlPath, err := exec.LookPath("systemctl")
	if err != nil {
		systemctlPath = "/usr/bin/systemctl"
	}

	content := fmt.Sprintf("%s ALL=(ALL) NOPASSWD: %s reload dnsmasq\n", user, systemctlPath)
	cmd := exec.Command("sudo", "tee", sudoersPath)
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("writing %s: %w\n%s", sudoersPath, err, out)
	}
	// sudoers files must not be world-writable
	exec.Command("sudo", "chmod", "0440", sudoersPath).Run() //nolint:errcheck
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
