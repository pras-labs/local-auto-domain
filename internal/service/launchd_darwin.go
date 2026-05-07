package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const plistLabel = "com.pras-labs.local-auto-domain"

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", plistLabel+".plist")
}

func Install(binaryPath string) error {
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>daemon</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/local-auto-domain.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/local-auto-domain.log</string>
</dict>
</plist>
`, plistLabel, binaryPath)

	path := plistPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(plist), 0644); err != nil {
		return err
	}

	out, err := exec.Command("launchctl", "load", "-w", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl load: %w\n%s", err, out)
	}
	fmt.Printf("Service installed: %s\nLogs: /tmp/local-auto-domain.log\n", plistLabel)
	return nil
}

func Uninstall() error {
	path := plistPath()
	// Unload (ignore error if not loaded)
	out, err := exec.Command("launchctl", "unload", "-w", path).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "Could not find") {
		return fmt.Errorf("launchctl unload: %w\n%s", err, out)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Printf("Service removed: %s\n", plistLabel)
	return nil
}

func Status() (string, error) {
	out, err := exec.Command("launchctl", "list", plistLabel).Output()
	if err != nil {
		return "not installed", nil
	}
	return string(out), nil
}
