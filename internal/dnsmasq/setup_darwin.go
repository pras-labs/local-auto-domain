package dnsmasq

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// dnsmasqPort is the port dnsmasq listens on. Using a non-privileged port lets
// dnsmasq run as the logged-in user (LaunchAgent) rather than root (LaunchDaemon),
// so the lad daemon can send SIGHUP without sudo. /etc/resolver/test includes the
// matching port directive so mDNSResponder routes queries to the right port.
const dnsmasqPort = 5300

// DropInDir returns the path where per-domain config files are written on macOS.
func DropInDir() string {
	// Support both Intel (/usr/local) and Apple Silicon (/opt/homebrew) Homebrew prefixes.
	for _, prefix := range []string{"/opt/homebrew", "/usr/local"} {
		if _, err := os.Stat(filepath.Join(prefix, "bin/brew")); err == nil {
			return filepath.Join(prefix, "etc/dnsmasq.d/local-auto-domain")
		}
	}
	return "/usr/local/etc/dnsmasq.d/local-auto-domain"
}

// brewPrefix returns the detected Homebrew prefix.
func brewPrefix() string {
	for _, prefix := range []string{"/opt/homebrew", "/usr/local"} {
		if _, err := os.Stat(filepath.Join(prefix, "bin/brew")); err == nil {
			return prefix
		}
	}
	return "/usr/local"
}

// Setup performs first-run dnsmasq configuration on macOS.
// Requires sudo for /etc/resolver write.
func Setup() error {
	prefix := brewPrefix()

	// 1. Install dnsmasq if missing
	if _, err := exec.LookPath("dnsmasq"); err != nil {
		fmt.Println("Installing dnsmasq via Homebrew...")
		if out, err := exec.Command(filepath.Join(prefix, "bin/brew"), "install", "dnsmasq").CombinedOutput(); err != nil {
			return fmt.Errorf("brew install dnsmasq: %w\n%s", err, out)
		}
	}

	// 2. Ensure dnsmasq.conf includes our drop-in dir
	dnsmasqConf := filepath.Join(prefix, "etc/dnsmasq.conf")
	dropInDir := DropInDir()
	confLine := fmt.Sprintf("conf-dir=%s,*.conf", dropInDir)
	if err := ensureLineInFile(dnsmasqConf, confLine); err != nil {
		return fmt.Errorf("updating dnsmasq.conf: %w", err)
	}

	// 3. Create drop-in dir owned by current user; write startup configs.
	if err := os.MkdirAll(dropInDir, 0755); err != nil {
		return fmt.Errorf("creating drop-in dir: %w", err)
	}
	// 00-port.conf: sets the listen port (read at startup, not on SIGHUP).
	portConf := filepath.Join(dropInDir, "00-port.conf")
	if err := os.WriteFile(portConf, []byte(fmt.Sprintf("port=%d\n", dnsmasqPort)), 0644); err != nil {
		return fmt.Errorf("writing port config: %w", err)
	}
	// addn-hosts: dnsmasq re-reads this file on SIGHUP, making it the mechanism
	// for dynamic domain updates without restarting dnsmasq.
	hostsFile := filepath.Join(dropInDir, "hosts")
	hostsLine := fmt.Sprintf("addn-hosts=%s", hostsFile)
	if err := ensureLineInFile(dnsmasqConf, hostsLine); err != nil {
		return fmt.Errorf("updating dnsmasq.conf with addn-hosts: %w", err)
	}
	// Create empty hosts file so dnsmasq doesn't log a warning at startup.
	if _, err := os.Stat(hostsFile); os.IsNotExist(err) {
		os.WriteFile(hostsFile, []byte{}, 0644) //nolint:errcheck
	}

	// 4. Write /etc/resolver/test with port directive (requires sudo)
	if err := writeResolver(); err != nil {
		return fmt.Errorf("writing resolver: %w", err)
	}

	// 5. Create loopback aliases 127.0.1.1–127.0.1.100 and persist via LaunchDaemon
	if err := setupLoopbackAliases(); err != nil {
		return fmt.Errorf("loopback aliases: %w", err)
	}

	// 6. Stop any system-level dnsmasq from a previous setup, then start as
	// the logged-in user so it runs as a LaunchAgent. This allows the lad daemon
	// to send SIGHUP without sudo.
	fmt.Println("Starting dnsmasq as current user...")
	brewBin := filepath.Join(prefix, "bin/brew")
	exec.Command(brewBin, "services", "stop", "dnsmasq").Run() //nolint:errcheck — best-effort
	if err := brewServicesStart(brewBin); err != nil {
		return fmt.Errorf("starting dnsmasq: %w", err)
	}

	fmt.Println("Setup complete. Domains under .tunnel.test are ready.")
	return nil
}

// setupLoopbackAliases creates 127.0.1.1–127.0.1.100 on lo0 immediately and
// installs a LaunchDaemon so they are recreated on every boot.
func setupLoopbackAliases() error {
	const count = 100
	fmt.Printf("Creating %d loopback aliases 127.0.1.1–127.0.1.%d (requires sudo)...\n", count, count)

	// Create immediately for current session
	for i := 1; i <= count; i++ {
		ip := fmt.Sprintf("127.0.1.%d", i)
		exec.Command("sudo", "ifconfig", "lo0", "alias", ip).Run()
	}

	// Build shell command for the LaunchDaemon
	shellCmd := fmt.Sprintf("for i in $(seq 1 %d); do /sbin/ifconfig lo0 alias 127.0.1.$i; done", count)
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.pras-labs.local-auto-domain-loopback</string>
    <key>ProgramArguments</key>
    <array>
        <string>/bin/bash</string>
        <string>-c</string>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
</dict>
</plist>
`, shellCmd)

	const daemonPath = "/Library/LaunchDaemons/com.pras-labs.local-auto-domain-loopback.plist"
	cmd := exec.Command("sudo", "tee", daemonPath)
	cmd.Stdin = strings.NewReader(plist)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("installing LaunchDaemon: %w\n%s", err, out)
	}
	exec.Command("sudo", "launchctl", "load", "-w", daemonPath).Run()
	fmt.Printf("LaunchDaemon installed: %s\n", daemonPath)
	return nil
}

// brewServicesStart starts dnsmasq as the logged-in user even when lad setup
// is invoked via sudo. Running as a user-level LaunchAgent (not a root LaunchDaemon)
// means the lad daemon can send SIGHUP without elevated privileges.
//
// sudo -u <user> is not sufficient: launchctl bootstrap gui/<uid> requires the
// process to hold the user's GUI session token, which sudo does not provide.
// launchctl asuser <uid> runs the command inside the user's session context and
// carries the correct token.
func brewServicesStart(brewBin string) error {
	sudoUser := os.Getenv("SUDO_USER")
	if sudoUser == "" {
		out, err := exec.Command(brewBin, "services", "start", "dnsmasq").CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w\n%s", err, out)
		}
		return nil
	}

	// Resolve the numeric UID — launchctl asuser requires it.
	uidBytes, err := exec.Command("id", "-u", sudoUser).Output()
	if err != nil {
		return fmt.Errorf("resolving UID for %s: %w", sudoUser, err)
	}
	uid := strings.TrimSpace(string(uidBytes))

	out, err := exec.Command("launchctl", "asuser", uid, brewBin, "services", "start", "dnsmasq").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// writeResolver writes /etc/resolver/test via sudo so macOS routes *.tunnel.test
// queries to dnsmasq on port 5300. Note: /etc/resolver/tunnel.localhost was the
// previous path; it is removed by Teardown but not written here.
func writeResolver() error {
	const resolverDir = "/etc/resolver"
	const resolverFile = "/etc/resolver/test"
	content := fmt.Sprintf("nameserver 127.0.0.1\nport %d\n", dnsmasqPort)

	// Check if already correct
	if existing, err := os.ReadFile(resolverFile); err == nil {
		if string(existing) == content {
			return nil
		}
	}

	fmt.Printf("Writing %s (requires sudo)...\n", resolverFile)

	// Create resolver dir via sudo if needed
	if _, err := os.Stat(resolverDir); errors.Is(err, os.ErrNotExist) {
		if out, err := exec.Command("sudo", "mkdir", "-p", resolverDir).CombinedOutput(); err != nil {
			return fmt.Errorf("sudo mkdir /etc/resolver: %w\n%s", err, out)
		}
	}

	// Write file via sudo tee
	cmd := exec.Command("sudo", "tee", resolverFile)
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sudo tee %s: %w\n%s", resolverFile, err, out)
	}
	return nil
}

// Teardown reverses Setup: removes loopback aliases, LaunchDaemon, resolver file,
// drop-in dir, and the conf-dir line from dnsmasq.conf. Requires sudo for system files.
func Teardown() error {
	prefix := brewPrefix()
	dropInDir := DropInDir()
	const loopbackDaemon = "/Library/LaunchDaemons/com.pras-labs.local-auto-domain-loopback.plist"

	// Unload + remove loopback LaunchDaemon
	exec.Command("sudo", "launchctl", "unload", "-w", loopbackDaemon).Run()
	exec.Command("sudo", "rm", "-f", loopbackDaemon).Run()

	// Remove loopback aliases
	fmt.Println("Removing loopback aliases 127.0.1.1–127.0.1.100...")
	for i := 1; i <= 100; i++ {
		exec.Command("sudo", "ifconfig", "lo0", "-alias", fmt.Sprintf("127.0.1.%d", i)).Run()
	}

	// Remove resolver files (current and legacy)
	exec.Command("sudo", "rm", "-f", "/etc/resolver/test").Run()
	exec.Command("sudo", "rm", "-f", "/etc/resolver/tunnel.localhost").Run()

	// Remove drop-in dir (user-owned)
	os.RemoveAll(dropInDir)

	// Remove conf-dir and addn-hosts lines from dnsmasq.conf (user-writable)
	dnsmasqConf := filepath.Join(prefix, "etc/dnsmasq.conf")
	removeLineFromFile(dnsmasqConf, fmt.Sprintf("conf-dir=%s,*.conf", dropInDir))
	removeLineFromFile(dnsmasqConf, fmt.Sprintf("addn-hosts=%s/hosts", dropInDir))

	// Stop dnsmasq: try system-level (old setup) then user-level (new setup).
	brewBin := filepath.Join(prefix, "bin/brew")
	exec.Command(brewBin, "services", "stop", "dnsmasq").Run() //nolint:errcheck
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		if uidBytes, err := exec.Command("id", "-u", sudoUser).Output(); err == nil {
			uid := strings.TrimSpace(string(uidBytes))
			exec.Command("launchctl", "asuser", uid, brewBin, "services", "stop", "dnsmasq").Run() //nolint:errcheck
		}
	}

	fmt.Println("dnsmasq configuration removed.")
	return nil
}

func removeLineFromFile(path, line string) {
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
	os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0644) //nolint:errcheck
}

func ensureLineInFile(path, line string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(data), line) {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n%s\n", line)
	return err
}
