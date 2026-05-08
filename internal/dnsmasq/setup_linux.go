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

// Teardown reverses Setup: removes DNS routing service, drop-in dir,
// sudoers rule, and the conf-dir line from /etc/dnsmasq.conf.
// Requires sudo for system files.
func Teardown() error {
	teardownSplitDNS()

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
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == strings.TrimSpace(line) {
			return nil
		}
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

const networkdLoConfig = "/etc/systemd/network/10-local-auto-domain-lo.network"

// configureSplitDNS routes .tunnel.test queries to dnsmasq on 127.0.0.1.
//
// Adding 127.0.0.1 to the global resolved scope (DNS= in resolved.conf.d)
// does not work: the global scope may have other DNS servers (e.g. 1.1.1.1
// from DHCP), and resolved uses whichever is "current" for the scope — not
// 127.0.0.1 specifically. NXDOMAIN from 1.1.1.1 is accepted; 127.0.0.1 is
// never tried.
//
// The fix requires an isolated per-link DNS scope on lo. How to achieve that
// depends on which network manager is active:
//   - systemd-networkd: write a .network file for lo; networkd registers the
//     per-link scope with resolved automatically at startup.
//   - NetworkManager: configure the loopback connection via nmcli; NM pushes
//     the per-link DNS to resolved when the connection comes up.
//   - dhcpcd / connman / ifupdown / unknown: no per-domain routing support;
//     write the .network file (inert) and print distro-specific guidance.
func configureSplitDNS() {
	switch {
	case isServiceActive("systemd-networkd"):
		configureSplitDNSNetworkd()
	case isServiceActive("NetworkManager"):
		configureSplitDNSNM()
	default:
		writeNetworkdLoConfig() //nolint:errcheck
		printSplitDNSFallbackHint()
	}
}

// printSplitDNSFallbackHint detects the active network manager and prints
// targeted guidance for configuring .tunnel.test DNS routing manually.
// None of these systems support per-domain routing natively, so the message
// tells the user what their stack is and what to do.
func printSplitDNSFallbackHint() {
	switch {
	case isServiceActive("dhcpcd"):
		fmt.Println("Detected: dhcpcd (no native per-domain DNS routing).")
		fmt.Println("To route .tunnel.test queries to dnsmasq, add to /etc/dhcpcd.conf:")
		fmt.Println("  nohook resolv.conf")
		fmt.Println("Then manage /etc/resolv.conf manually or enable systemd-networkd.")

	case isServiceActive("connman"):
		fmt.Println("Detected: connman (no native per-domain DNS routing).")
		fmt.Println("ConnMan proxies all DNS — it does not support routing by domain.")
		fmt.Println("Options: enable systemd-networkd for lo, or disable ConnMan's DNS")
		fmt.Println("proxy (dns=off in main.conf) and point /etc/resolv.conf at dnsmasq.")

	case commandExists("ifup"):
		fmt.Println("Detected: ifupdown (DNS managed via /etc/resolv.conf or resolvconf).")
		if commandExists("resolvconf") {
			fmt.Println("resolvconf found. Add to /etc/resolvconf/resolv.conf.d/head:")
			fmt.Println("  nameserver 127.0.0.1")
			fmt.Println("Then run: sudo resolvconf -u")
			fmt.Println("Note: this prepends dnsmasq to the resolver list, not true split-DNS.")
		} else {
			fmt.Println("Add 'nameserver 127.0.0.1' as the first line of /etc/resolv.conf.")
			fmt.Println("Note: this routes ALL DNS through dnsmasq (not just .tunnel.test).")
			fmt.Println("Configure dnsmasq upstream forwarding in /etc/dnsmasq.conf:")
			fmt.Println("  server=8.8.8.8")
		}

	default:
		fmt.Println("Note: no recognised network manager detected.")
		fmt.Println(".tunnel.test DNS routing config written to:")
		fmt.Printf("  %s\n", networkdLoConfig)
		fmt.Println("Start systemd-networkd to activate it, or configure DNS routing manually.")
	}
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func isServiceActive(name string) bool {
	out, _ := exec.Command("systemctl", "is-active", name).Output()
	return strings.TrimSpace(string(out)) == "active"
}

// configureSplitDNSNetworkd writes a .network file for lo and reloads networkd.
func configureSplitDNSNetworkd() {
	if err := writeNetworkdLoConfig(); err != nil {
		fmt.Printf("Warning: could not write networkd lo config: %v\n", err)
		return
	}
	exec.Command("sudo", "networkctl", "reload").Run()
}

func writeNetworkdLoConfig() error {
	const content = "[Match]\nName=lo\n\n[Network]\nDNS=127.0.0.1\nDomains=~tunnel.test\n"
	exec.Command("sudo", "mkdir", "-p", "/etc/systemd/network").Run()
	cmd := exec.Command("sudo", "tee", networkdLoConfig)
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// configureSplitDNSNM configures the loopback connection in NetworkManager so
// NM pushes 127.0.0.1 + ~tunnel.test to systemd-resolved as a per-link scope.
// NM is assumed to use dns=systemd-resolved (the default on modern distros).
func configureSplitDNSNM() {
	// Find the active connection on the lo interface.
	out, err := exec.Command("nmcli", "-t", "-f", "NAME,DEVICE", "connection", "show", "--active").Output()
	if err != nil {
		fmt.Printf("Warning: nmcli failed: %v\n", err)
		return
	}
	var connName string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[1]) == "lo" {
			connName = strings.TrimSpace(parts[0])
			break
		}
	}
	if connName == "" {
		fmt.Println("Warning: no active NetworkManager connection found on lo.")
		fmt.Println("Falling back to networkd config (inert until networkd is started).")
		writeNetworkdLoConfig() //nolint:errcheck
		return
	}

	exec.Command("nmcli", "connection", "modify", connName,
		"ipv4.dns", "127.0.0.1",
		"ipv4.dns-search", "~tunnel.test").Run()
	exec.Command("nmcli", "connection", "up", connName).Run()
}

// teardownSplitDNS reverses configureSplitDNS for whichever method was used.
func teardownSplitDNS() {
	// networkd path
	exec.Command("sudo", "rm", "-f", networkdLoConfig).Run()
	if isServiceActive("systemd-networkd") {
		exec.Command("sudo", "networkctl", "reload").Run()
	}

	// NM path — clear DNS from loopback connection if NM is active
	if isServiceActive("NetworkManager") {
		out, err := exec.Command("nmcli", "-t", "-f", "NAME,DEVICE", "connection", "show", "--active").Output()
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 && strings.TrimSpace(parts[1]) == "lo" {
					connName := strings.TrimSpace(parts[0])
					exec.Command("nmcli", "connection", "modify", connName,
						"ipv4.dns", "",
						"ipv4.dns-search", "").Run()
					exec.Command("nmcli", "connection", "up", connName).Run()
					break
				}
			}
		}
	}

	// Legacy artifacts
	exec.Command("sudo", "rm", "-f", "/etc/systemd/system/local-auto-domain-dns.service").Run()
	exec.Command("sudo", "systemctl", "daemon-reload").Run()
	exec.Command("sudo", "rm", "-f", "/etc/systemd/resolved.conf.d/local-auto-domain.conf").Run()
}
